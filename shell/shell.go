// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package shell runs a shell session whose commands, files and environment come
// from a [Transport] rather than from this machine.
package shell

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/reeflective/readline"

	xio "unikraft.com/x/io"
	"unikraft.com/x/log"
)

const (
	instanceRestartTimeout = 90 * time.Second
	instanceRestartPoll    = time.Second

	instanceProbeTimeout  = 3 * time.Second
	instanceProbeAttempts = 2
)

// environFallbacks fill in what an instance did not export. PATH is what a
// container image starts with when it sets none (Debian's ENV_SUPATH), so a
// shell finds what the image's own processes find; HOME and the ids are an
// instance's reality: one user, root, at the root of its filesystem.
var environFallbacks = map[string]string{
	"HOME": "/",
	"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"UID":  "0",
	"EUID": "0",
	"GID":  "0",
}

var (
	errNotATerminal       = errors.New("the shell needs a terminal; use -c '<command line>' to run commands without one")
	errNotProcessTerminal = errors.New("the shell prompts only on the process's own terminal; use -c '<command line>' on any other")

	// Statements, not files: running a whole file is an exit to the
	// interpreter, and would fire the EXIT trap on every ^C or restart.
	dirReset       = mustParse("cd /").Stmts[0]
	interruptReset = mustParse(fmt.Sprintf("(exit %d)", StatusInterrupted)).Stmts[0]

	// sessionEnd is the exit ^D asks for: an empty file, which to the
	// interpreter is an exit, so the EXIT trap runs once, now.
	sessionEnd = mustParse("")
)

// console is where the session writes, serialised so two commands cannot interleave a write.
type console struct {
	Out, Err io.Writer
}

type session struct {
	cfg     Config
	console console

	runner *interp.Runner

	interactive bool

	// noShell is set when the probe found no sh on the instance: commands still
	// run by argv, but nothing that needs a shell over there can.
	noShell bool

	commands []string

	tty *os.File

	// profile is the colours the caller's terminal takes, for what readline
	// paints past the console: the prompt and the line being typed.
	profile colorprofile.Profile

	editor *prompt

	restarted atomic.Bool
}

func (s *session) dir() string {
	return s.runner.Dir
}

func Run(ctx context.Context, cfg Config, streams Streams) (int, error) {
	if cfg.Transport == nil {
		return 0, fmt.Errorf("no transport to the instance")
	}
	for _, name := range sessionBuiltinNames {
		if _, taken := cfg.Builtins[name]; taken {
			return 0, fmt.Errorf("builtin %q is the session's to provide", name)
		}
	}
	if cfg.Dir == "" {
		cfg.Dir = "/"
	}

	s := &session{cfg: cfg}
	if f, ok := streams.In.(*os.File); ok && xio.IsTTYReader(f) {
		s.tty = f
	}
	onTerminal := cfg.Command == "" && s.tty != nil && xio.IsTTY(streams.Out)
	s.interactive = onTerminal && isProcessStdio(s.tty, streams.Out)
	s.profile = profileOf(streams.Out)

	var terminal sync.Mutex
	s.console = console{
		Out: &lockedWriter{mu: &terminal, w: streams.Out},
		Err: &lockedWriter{mu: &terminal, w: streams.Err},
	}

	env, err := s.environ(ctx)
	if err != nil {
		return 0, err
	}

	runner, err := interp.New(
		interp.StdIO(streams.In, s.console.Out, s.console.Err),
		interp.Env(expand.ListEnviron(env...)),
		interp.Interactive(s.interactive),
		interp.CallHandler(keepLookupsRemote),
		interp.ExecHandlers(s.route),
		interp.StatHandler(s.statHandler),
		interp.AccessHandler(s.accessHandler),
		interp.ReadDirHandler2(s.readDirHandler),
		interp.OpenHandler(s.open),
	)
	if err != nil {
		return 0, err
	}

	runner.Dir = cfg.Dir
	runner.Reset()
	s.runner = runner

	switch {
	case cfg.Command != "":
		return s.runSource(ctx, strings.NewReader(cfg.Command))
	case s.interactive:
		return s.runInteractive(ctx)
	case onTerminal:
		return 0, errNotProcessTerminal
	default:
		return 0, errNotATerminal
	}
}

func (s *session) runSource(ctx context.Context, src io.Reader) (int, error) {
	prog, err := syntax.NewParser().Parse(src, "")
	if err != nil {
		return 0, err
	}
	if err := unsupported(prog); err != nil {
		return 0, err
	}
	return exitStatus(s.runner.Run(ctx, prog))
}

// exitStatus separates a command's exit status from the interpreter failing.
func exitStatus(err error) (int, error) {
	if status, ok := errors.AsType[interp.ExitStatus](err); ok {
		return int(status), nil
	}
	return 0, err
}

// unsupported finds what the interpreter would have to answer from this machine
// rather than the instance
func unsupported(prog *syntax.File) error {
	var err error
	syntax.Walk(prog, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.ProcSubst:
			err = fmt.Errorf("%s: process substitution is not supported, the pipe would be on this machine", n.Pos())
		case *syntax.Word:
			if len(n.Parts) == 0 {
				break
			}
			// A word opening with ~ and a name: bare ~ and ~/ are $HOME, and fine.
			if lit, ok := n.Parts[0].(*syntax.Lit); ok && len(lit.Value) > 1 && lit.Value[0] == '~' &&
				(lit.Value[1] == '_' || unicode.IsLetter(rune(lit.Value[1]))) {
				err = fmt.Errorf("%s: ~user is not supported, it would be this machine's user", n.Pos())
			}
		}
		return err == nil
	})
	return err
}

func (s *session) runInteractive(ctx context.Context) (int, error) {
	sigint, stop := captureInterrupts(s.cfg.SuspendSignals)
	defer stop()
	defer s.plainKeys()()

	if len(s.cfg.Banner) > 0 {
		fmt.Fprintln(s.console.Err, bannerStyle.Render(strings.Join(s.cfg.Banner, "\n")))
	}

	s.editor = s.newPrompt(ctx)

	parser := syntax.NewParser()

	for {
		line, err := s.editor.readLine(ctx)
		switch {
		case errors.Is(err, readline.ErrInterrupt):
			fmt.Fprintln(s.console.Out, hintStyle.Render("^C"))
			continue
		case errors.Is(err, io.EOF):
			fmt.Fprintln(s.console.Out)
			_ = s.runner.Run(ctx, sessionEnd)
			return 0, nil
		case err != nil:
			return 0, err
		}

		prog, err := parser.Parse(strings.NewReader(line+"\n"), "")
		if err == nil {
			err = cmp.Or(unsupported(prog), noJobControl(prog))
		}
		if err != nil {
			fmt.Fprintln(s.console.Err, errorStyle.Render(err.Error()))
			continue
		}

		for _, stmt := range prog.Stmts {
			status, interrupted, err := s.runStmt(ctx, sigint, stmt)
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if err != nil {
				fmt.Fprintln(s.console.Err, errorStyle.Render(err.Error()))
			}
			if s.restarted.Swap(false) {
				s.relocate(ctx)
			}
			if interrupted {
				// The terminal echoed the ^C where the command's output stopped;
				// the prompt goes on a line of its own.
				fmt.Fprintln(s.console.Out)
				break
			}
			if s.runner.Exited() {
				return status, nil
			}
		}
	}
}

// noJobControl refuses what the prompt cannot run: a statement sent to the
// background lives on the interpreter's context for that line, which the prompt
// cancels as soon as the line is done.
func noJobControl(prog *syntax.File) error {
	var err error
	syntax.Walk(prog, func(node syntax.Node) bool {
		if stmt, ok := node.(*syntax.Stmt); ok && (stmt.Background || stmt.Coprocess) {
			err = fmt.Errorf("%s: no job control, & is not supported at the prompt", stmt.Pos())
		}
		return err == nil
	})
	return err
}

// runStmt runs one statement, watching for the interrupt that ends it early.
func (s *session) runStmt(ctx context.Context, sigint <-chan os.Signal, stmt *syntax.Stmt) (status int, interrupted bool, err error) {
	for drained := false; !drained; {
		select {
		case <-sigint:
		default:
			drained = true
		}
	}

	stmtCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		hit  atomic.Bool
		wg   sync.WaitGroup
		done = make(chan struct{})
	)

	stop := sync.OnceFunc(func() {
		close(done)
		wg.Wait()
	})
	defer stop()

	wg.Go(func() {
		for {
			select {
			case <-sigint:
				hit.Store(true)
				cancel()
			case <-done:
				return
			}
		}
	})

	status, err = exitStatus(s.runner.Run(stmtCtx, stmt))
	stop()

	if !hit.Load() {
		return status, false, err
	}
	s.clearInterrupt(ctx)
	return StatusInterrupted, true, nil
}

func (s *session) settle(ctx context.Context) bool {
	s.commands = nil

	fmt.Fprintln(s.console.Err, hintStyle.Render("the instance is restarting; waiting for it"))

	waitCtx, cancel := context.WithTimeout(ctx, instanceRestartTimeout)
	defer cancel()

	for !s.answers(waitCtx) {
		switch {
		case ctx.Err() != nil:
			fmt.Fprintln(s.console.Err, hintStyle.Render("stopped waiting for the instance"))
			return false
		case waitCtx.Err() != nil:
			fmt.Fprintln(s.console.Err, errorStyle.Render(
				`the instance has not come back; try ":get" or reconnect`))
			return true
		}
		select {
		case <-time.After(instanceRestartPoll):
		case <-waitCtx.Done():
		}
	}
	return true
}

func (s *session) relocate(ctx context.Context) {
	if s.dir() == probeDir {
		return
	}
	if _, err := s.stat(ctx, probeDir, s.dir(), true); err == nil {
		return
	}

	fmt.Fprintln(s.console.Err, hintStyle.Render(
		"the working directory did not survive the restart; moved to "+probeDir))
	_ = s.runner.Run(ctx, dirReset)
}

func (s *session) answers(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, instanceProbeTimeout)
	defer cancel()

	_, err := s.script(probeCtx, `:`)
	return err == nil
}

func mustParse(src string) *syntax.File {
	prog, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		panic(err)
	}
	return prog
}

func (s *session) clearInterrupt(ctx context.Context) {
	_ = s.runner.Run(ctx, interruptReset)
}

func (s *session) prompt(continuation bool) string {
	if continuation {
		return s.paint(continuationStyle.Render("> "))
	}
	return s.paint(promptStyle.Render(s.cfg.Instance) +
		promptDirStyle.Render(":"+s.dir()) +
		promptStyle.Render("$ "))
}

// paint brings styled text down to the caller's colour profile: what goes
// through the console is converted by the caller's writer, what readline paints
// itself is not.
func (s *session) paint(styled string) string {
	if s.profile == colorprofile.TrueColor {
		return styled
	}
	var buf strings.Builder
	_, _ = (&colorprofile.Writer{Forward: &buf, Profile: s.profile}).Write([]byte(styled))
	return buf.String()
}

// profileOf is the colour profile of the caller's stdout: the one its writer
// already converts to, or the terminal's own.
func profileOf(w io.Writer) colorprofile.Profile {
	if cw, ok := w.(*colorprofile.Writer); ok {
		return cw.Profile
	}
	return colorprofile.Detect(w, os.Environ())
}

func (s *session) plainKeys() func() {
	fmt.Fprint(s.console.Out, ansi.DisableKittyKeyboard, ansi.ResetModifyOtherKeys)
	return func() {
		fmt.Fprint(s.console.Out, ansi.PopKittyKeyboard(1), ansi.ResetModifyOtherKeys)
	}
}

func captureInterrupts(suspend SuspendFunc) (<-chan os.Signal, func()) {
	restore := func() {}
	if suspend != nil {
		restore = suspend(syscall.SIGINT)
	}

	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGINT)

	return ch, func() {
		signal.Stop(ch)
		restore()
	}
}

// isProcessStdio reports whether in and out are the process' own stdin and
// stdout, the only streams readline can edit on.
func isProcessStdio(in *os.File, out io.Writer) bool {
	f, ok := xio.Unwrap(out).(*os.File)
	return ok && sameFile(in, os.Stdin) && sameFile(f, os.Stdout)
}

func sameFile(a, b *os.File) bool {
	ai, err := a.Stat()
	if err != nil {
		return false
	}
	bi, err := b.Stat()
	return err == nil && os.SameFile(ai, bi)
}

// keepLookupsRemote stops type, command -v and pwd -P from running locally
func keepLookupsRemote(_ context.Context, args []string) ([]string, error) {
	var local bool
	switch args[0] {
	case "type":
		local = true
	case "command":
		local = len(args) > 1 && (args[1] == "-v" || args[1] == "-V")
	case "pwd":
		local = slices.Contains(args[1:], "-P")
	}
	if !local {
		return args, nil
	}
	return append([]string{"sh", "-c", args[0] + ` "$@"`, "sh"}, args[1:]...), nil
}

func (s *session) environ(ctx context.Context) ([]string, error) {
	out, err := s.probe(ctx)
	if err != nil {
		log.G(ctx).Debug().Err(err).Msg("could not read the instance environment")

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("could not reach %s: %w", cmp.Or(s.cfg.Instance, "the instance"), err)
	}

	vars := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && isEnvName(name) {
			vars[name] = value
		}
	}
	maps.Copy(vars, s.cfg.Env)

	for name, fallback := range environFallbacks {
		if vars[name] == "" {
			vars[name] = fallback
		}
	}

	env := make([]string, 0, len(vars))
	for name, value := range vars {
		env = append(env, name+"="+value)
	}
	slices.Sort(env)
	return env, nil
}

func (s *session) probe(ctx context.Context) (string, error) {
	var err error
	for range instanceProbeAttempts {
		var out bytes.Buffer

		probeCtx, cancel := context.WithTimeout(ctx, instanceProbeTimeout)
		probeCtx = Detached(probeCtx)

		var code int
		code, err = s.cfg.Transport.Exec(probeCtx, Streams{Out: &out, Err: io.Discard}, s.cfg.Dir, nil,
			[]string{"sh", "-c", environProbe})
		cancel()

		switch {
		case err == nil:
			s.noShell = code == statusBuiltinNotFound
			return out.String(), nil
		case ctx.Err() != nil:
			return "", err
		}
	}
	return "", err
}

func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func (s *session) builtinNames() []string {
	names := slices.AppendSeq(slices.Clone(sessionBuiltinNames), maps.Keys(s.cfg.Builtins))
	slices.Sort(names)
	return names
}

// lockedWriter serialises writes from commands running at the same time.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
