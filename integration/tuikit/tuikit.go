// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package tuikit drives a Bubble Tea model the way a person at a terminal
// does: it presses keys, and reads the screen.
//
// The model runs in a real tea.Program, attached to a terminal emulator
// instead of a TTY. Keys go in as the bytes a terminal sends, and a snapshot
// is what the emulator has on its screen once it has parsed everything the
// program wrote. Nothing calls Update or View directly.
//
// A test waits for what it expects on the screen, or on a Region of it: a
// line, or some text.
package tuikit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// ErrStopped is the error of a wait on a program that has ended.
var ErrStopped = errors.New("tuikit: the program has ended")

// syncPrefix opens the escape sequence that sync sends through the input, and
// that the program hands back as an unknown event.
const syncPrefix = "\x1b_tuikit-sync:"

// Runner runs a Bubble Tea model in a terminal emulator.
//
// A test starts one with Start, sends input with Press and Type, and waits
// for the screen with WaitMatch. Anything else creates one with New, and
// calls Stop when it's done.
type Runner struct {
	// Region is the whole screen.
	*Region

	width   int
	height  int
	timeout time.Duration

	program *tea.Program
	term    *vt.SafeEmulator
	input   *queue

	done   chan struct{}
	err    error
	stop   sync.Once
	killed atomic.Bool
	// quit is set once the program has been told to quit, which is before it
	// clears the screen on its way out.
	quit atomic.Bool
	// blank is whether the last view of the model had nothing to show.
	blank atomic.Bool

	mu sync.Mutex
	// drawn is closed, and replaced, each time the program writes to the terminal.
	drawn chan struct{}
	// syncs are the sync sequences that the program has not handed back yet.
	syncs  map[string]chan struct{}
	syncID int
	// shown is the last screen that a wait saw while the program ran.
	shown screen
}

// Option configures a Runner.
type Option func(*Runner)

// WithSize sets the size of the terminal, which is 80x24 by default.
func WithSize(width, height int) Option {
	return func(r *Runner) {
		r.width, r.height = width, height
	}
}

// WithTimeout sets how long Wait and its variants wait before failing
// the test, which is 5 seconds by default.
func WithTimeout(timeout time.Duration) Option {
	return func(r *Runner) {
		r.timeout = timeout
	}
}

// New starts model in a new terminal, and returns once it has drawn.
func New(model tea.Model, opts ...Option) *Runner {
	r := &Runner{
		width:   80,
		height:  24,
		timeout: 5 * time.Second,
		input:   newQueue(),
		done:    make(chan struct{}),
		drawn:   make(chan struct{}),
		syncs:   map[string]chan struct{}{},
	}
	for _, opt := range opts {
		opt(r)
	}
	r.Region = &Region{r: r}

	r.term = vt.NewSafeEmulator(r.width, r.height)
	// What the terminal sends: the keys, and its answers to the program's queries.
	go func() { _, _ = io.Copy(r.input, r.term) }()

	r.program = tea.NewProgram(framed{model, r},
		tea.WithInput(r.input),
		tea.WithOutput(output{r}),
		tea.WithWindowSize(r.width, r.height),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}),
		tea.WithColorProfile(colorprofile.TrueColor),
		tea.WithoutSignals(),
		tea.WithFilter(r.filter),
	)
	go func() {
		_, r.err = r.program.Run()
		close(r.done)
		r.redrawn()
	}()

	// The screen is empty until the first frame, unless that frame is empty.
	_ = r.WaitUntil(r.timeout, func(snapshot string) bool { return snapshot != "" || r.blank.Load() })

	return r
}

// framed is the model of a Runner, which it tells about each of its views.
type framed struct {
	tea.Model
	r *Runner
}

func (f framed) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := f.Model.Update(msg)
	return framed{model, f.r}, cmd
}

func (f framed) View() tea.View {
	view := f.Model.View()
	f.r.blank.Store(strings.TrimSpace(view.Content) == "")
	f.r.redrawn()

	return view
}

// PressKeys presses each of keys, and returns once the program has updated
// for all of them. It fails on a key that a terminal has no bytes for, such
// as super+a.
//
// The screen follows later: the program draws on its next frame, and a
// command that an update returns runs on its own time. So wait for what the
// key puts on the screen, with WaitUntil, before taking a snapshot.
func (r *Runner) PressKeys(keys ...tea.Key) error {
	for _, key := range keys {
		event := vt.KeyPressEvent(uv.Key(key))
		seq, ok := xtermKey(key)
		switch {
		case introduces(key):
			return fmt.Errorf("tuikit: a terminal cannot send %s", tea.KeyPressMsg(key))
		case encodes(event):
			r.term.SendKey(event)
		case ok:
			r.term.SendText(seq)
		default:
			return fmt.Errorf("tuikit: a terminal cannot send %s", tea.KeyPressMsg(key))
		}
		// Each key waits for the program, so that it does not read an escape
		// as the alt of the key after it.
		if err := r.sync(); err != nil {
			return err
		}
	}

	return nil
}

// introduces reports whether the key is sent as an escape and a character
// that starts a longer sequence, which the program would wait for the rest of.
func introduces(key tea.Key) bool {
	c := key.Code
	if key.Mod.Contains(tea.ModShift) {
		c = unicode.ToUpper(c)
	}
	return key.Mod.Contains(tea.ModAlt) && strings.ContainsRune("[]OPX^_", c)
}

// encodes reports whether the emulator has bytes for the key.
//
// HACK: its SendKey sends nothing for a key it does not know, so ask one.
func encodes(key vt.KeyPressEvent) bool {
	// Alt is only an escape before the rest, which is sent even when the rest
	// is not.
	key.Mod &^= uv.ModAlt

	probe := vt.NewEmulator(1, 1)
	go probe.SendKey(key)
	n, _ := probe.Read(make([]byte, 64))

	return n > 0
}

// xtermKey returns what xterm sends for the keys that the emulator leaves
// out: a shifted letter, and a cursor or editing key with modifiers.
//
// HACK: the rest cannot be sent, such as shift+enter, which needs the kitty
// keyboard protocol.
func xtermKey(key tea.Key) (string, bool) {
	if key.Mod&^(tea.ModShift|tea.ModAlt|tea.ModCtrl) != 0 {
		return "", false
	}
	if key.Mod&^tea.ModAlt == tea.ModShift && unicode.IsLetter(key.Code) {
		upper := string(unicode.ToUpper(key.Code))
		if key.Mod.Contains(tea.ModAlt) {
			return "\x1b" + upper, true
		}
		return upper, true
	}

	mod := 1
	if key.Mod.Contains(tea.ModShift) {
		mod += 1
	}
	if key.Mod.Contains(tea.ModAlt) {
		mod += 2
	}
	if key.Mod.Contains(tea.ModCtrl) {
		mod += 4
	}
	if final, ok := map[rune]byte{
		tea.KeyUp: 'A', tea.KeyDown: 'B', tea.KeyRight: 'C', tea.KeyLeft: 'D',
		tea.KeyHome: 'H', tea.KeyEnd: 'F',
	}[key.Code]; ok {
		return fmt.Sprintf("\x1b[1;%d%c", mod, final), true
	}
	if num, ok := map[rune]int{
		tea.KeyInsert: 2, tea.KeyDelete: 3, tea.KeyPgUp: 5, tea.KeyPgDown: 6,
	}[key.Code]; ok {
		return fmt.Sprintf("\x1b[%d;%d~", num, mod), true
	}

	return "", false
}

// TypeText types text, and returns once the program has updated for it. As
// with PressKeys, the screen follows later.
func (r *Runner) TypeText(text string) error {
	r.term.SendText(text)
	return r.sync()
}

// screen returns what the terminal shows now.
func (r *Runner) screen() screen {
	// HACK: not the emulator's CellAt, whose cells are read after its lock.
	// https://github.com/charmbracelet/x/pull/1054
	buf := uv.NewScreenBuffer(r.width, r.height)
	r.term.Draw(buf, buf.Bounds())

	s := make(screen, r.height)
	for y := range s {
		s[y] = make([]cell, r.width)
		for x := range s[y] {
			if c := buf.CellAt(x, y); c != nil {
				s[y][x] = cell{text: c.Content}
			}
		}
	}
	return s
}

// WaitUntil blocks until pred returns true for the current Snapshot. It
// checks again each time the program draws, and gives up with
// context.DeadlineExceeded after timeout, or with ErrStopped if the program
// ends first.
func (r *Runner) WaitUntil(timeout time.Duration, pred func(snapshot string) bool) error {
	return r.waitScreen(timeout, func(s screen) bool { return pred(s.text(s.bounds())) })
}

func (r *Runner) waitScreen(timeout time.Duration, pred func(screen) bool) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		r.mu.Lock()
		drawn := r.drawn
		r.mu.Unlock()

		// A program that has ended has cleared its screen, or is about to:
		// that is not a screen to wait on.
		s := r.screen()
		if r.quit.Load() {
			<-r.done
		}
		if err := r.Err(); err != nil {
			return err
		}
		r.mu.Lock()
		r.shown = s
		r.mu.Unlock()
		if pred(s) {
			return nil
		}

		select {
		case <-drawn:
		case <-timer.C:
			return context.DeadlineExceeded
		case <-r.done:
			return r.Err()
		}
	}
}

// last returns the last screen that a wait saw while the program ran.
func (r *Runner) last() screen {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.shown
}

// Err returns nil while the program runs, and after that an error that wraps
// ErrStopped, with the reason if the program did not just quit.
func (r *Runner) Err() error {
	select {
	case <-r.done:
	default:
		return nil
	}
	if r.err != nil && !r.killed.Load() {
		return fmt.Errorf("%w: %w", ErrStopped, r.err)
	}
	return ErrStopped
}

// Stop ends the program, if it still runs, and closes the terminal.
func (r *Runner) Stop() {
	r.stop.Do(func() {
		r.killed.Store(true)
		r.program.Kill()
		<-r.done
		r.input.Close()
		// HACK: not the emulator's Close, whose closed flag races with its Read.
		// https://github.com/charmbracelet/x/pull/1054
		if pipe, ok := r.term.InputPipe().(io.Closer); ok {
			_ = pipe.Close()
		}
	})
}

// sync waits for the program to have updated for all the input so far. A
// program that has ended takes no input, which is not an error.
func (r *Runner) sync() error {
	r.mu.Lock()
	r.syncID++
	id := strconv.Itoa(r.syncID)
	seen := make(chan struct{})
	r.syncs[id] = seen
	r.mu.Unlock()

	// The sequence goes through the same input as the keys, so the program
	// reads it after them.
	r.term.SendText(syncPrefix + id + "\x1b\\")
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case <-seen:
	case <-r.done:
	case <-timer.C:
		return fmt.Errorf("tuikit: the program did not update for its input within %s", r.timeout)
	}

	return nil
}

// filter takes the sequences of sync out of the program's messages.
func (r *Runner) filter(_ tea.Model, msg tea.Msg) tea.Msg {
	if _, ok := msg.(tea.QuitMsg); ok {
		r.quit.Store(true)
	}
	apc, ok := msg.(uv.UnknownApcEvent)
	if !ok || !strings.HasPrefix(string(apc), syncPrefix) {
		return msg
	}
	id := strings.TrimSuffix(strings.TrimPrefix(string(apc), syncPrefix), "\x1b\\")

	r.mu.Lock()
	if seen, ok := r.syncs[id]; ok {
		close(seen)
		delete(r.syncs, id)
	}
	r.mu.Unlock()

	return nil
}

func (r *Runner) redrawn() {
	r.mu.Lock()
	close(r.drawn)
	r.drawn = make(chan struct{})
	r.mu.Unlock()
}

// output is the program's output: the terminal of a Runner.
type output struct{ r *Runner }

func (s output) Write(p []byte) (int, error) {
	// The program is not on a TTY, so it leaves the carriage return of a
	// new line to the line discipline that a TTY would have. Windows has
	// none, and there the program returns the carriage itself.
	out := p
	if runtime.GOOS != "windows" {
		out = bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))
	}
	if _, err := s.r.term.Write(out); err != nil {
		return 0, err
	}
	s.r.redrawn()

	return len(p), nil
}

// queue is the program's input: a pipe that never blocks its writer, so the
// terminal can always answer, also to a program that has stopped reading.
type queue struct {
	mu     sync.Mutex
	more   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.more = sync.NewCond(&q.mu)
	return q
}

func (q *queue) Write(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return 0, io.ErrClosedPipe
	}
	q.buf.Write(p)
	q.more.Broadcast()

	return len(p), nil
}

func (q *queue) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for q.buf.Len() == 0 {
		if q.closed {
			return 0, io.EOF
		}
		q.more.Wait()
	}

	return q.buf.Read(p)
}

func (q *queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.closed = true
	q.more.Broadcast()

	return nil
}
