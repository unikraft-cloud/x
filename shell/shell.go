// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

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
	errNotATerminal       = errors.New("the shell needs a terminal to prompt on; give it a command line to run without one")
	errNotProcessTerminal = errors.New("the shell prompts only on the process's own terminal; on any other, give it a command line")

	// A statement, not a file: a whole file is an exit to the interpreter
	interruptReset = mustParse(fmt.Sprintf("(exit %d)", StatusInterrupted)).Stmts[0]

	// sessionEnd is the exit ^D asks for
	sessionEnd = mustParse("")
)

// console is where the session writes, serialised so two commands cannot interleave a write.
type console struct {
	Out, Err io.Writer
}

type session struct {
	cfg         Config
	console     console
	runner      *interp.Runner
	parser      *syntax.Parser
	interactive bool
	onTerminal  bool
	noShell     bool
	commands    []string
	tty         *os.File
	stdin       *os.File
	profile     colorprofile.Profile
	editor      *prompt
	history     *sessionHistory
	interrupts  chan os.Signal
	exiting     atomic.Bool
}

func (s *session) dir() string {
	return s.runner.Dir
}

func Run(ctx context.Context, cfg Config, streams Streams) (int, error) {
	s, err := newSession(ctx, cfg, streams)
	if err != nil {
		return 0, err
	}

	switch {
	case s.cfg.Command != "":
		return s.runSource(ctx, strings.NewReader(s.cfg.Command))
	case s.interactive:
		return s.runInteractive(ctx)
	case s.onTerminal:
		return 0, errNotProcessTerminal
	default:
		return 0, errNotATerminal
	}
}

// newSession initializes the streams it writes on, the instance's environment,
// and the interpreter over both.
func newSession(ctx context.Context, cfg Config, streams Streams) (*session, error) {
	if cfg.Transport == nil {
		return nil, fmt.Errorf("no transport to the instance")
	}
	for _, name := range sessionBuiltinNames {
		if _, taken := cfg.Builtins[name]; taken {
			return nil, fmt.Errorf("builtin %q is the session's to provide", name)
		}
	}
	if cfg.Dir == "" {
		cfg.Dir = "/"
	}

	s := &session{cfg: cfg, parser: syntax.NewParser(), interrupts: make(chan os.Signal, 4)}
	if f, ok := streams.In.(*os.File); ok && xio.IsTTYReader(f) {
		s.tty = f
	}
	s.stdin = cmp.Or(s.tty, cfg.Input)
	s.onTerminal = cfg.Command == "" && s.tty != nil && xio.IsTTY(streams.Out)
	s.interactive = s.onTerminal && isProcessStdio(s.tty, streams.Out)
	s.profile = profileOf(streams.Out)

	var terminal sync.Mutex
	s.console = console{
		Out: lockWriter(&terminal, streams.Out),
		Err: lockWriter(&terminal, streams.Err),
	}

	env, err := s.environ(ctx)
	if err != nil {
		return nil, err
	}

	in := streams.In
	if cfg.Input != nil {
		in = cfg.Input
	}

	runner, err := interp.New(
		interp.StdIO(in, s.console.Out, s.console.Err),
		interp.Env(expand.ListEnviron(env...)),
		interp.Interactive(s.interactive),
		interp.CallHandler(s.call),
		interp.ExecHandlers(s.route),
		interp.StatHandler(s.statHandler),
		interp.AccessHandler(s.accessHandler),
		interp.ReadDirHandler2(s.readDirHandler),
		interp.OpenHandler(s.open),
	)
	if err != nil {
		return nil, err
	}

	runner.Dir = cfg.Dir
	runner.Reset()
	s.runner = runner
	return s, nil
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
// and not the instance
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
	stop := s.captureInterrupts()
	defer stop()
	defer s.plainKeys()()

	if len(s.cfg.Banner) > 0 {
		fmt.Fprintln(s.console.Err, bannerStyle.Render(strings.Join(s.cfg.Banner, "\n")))
	}

	s.history = &sessionHistory{}
	s.editor = s.newPrompt(ctx)

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

		status, exited, err := s.runInput(ctx, line)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil {
			continue
		}
		if exited {
			return status, nil
		}
	}
}

// runInput runs what the caller typed, and reports whether the session is to
// end. A line the shell will not run is told on its own stderr and reported
// back as well, so that a caller can tell it from a command that failed.
func (s *session) runInput(ctx context.Context, line string) (status int, exited bool, err error) {
	// readline records what it accepts itself, so only a driven session's lines
	// are on the session to keep.
	if s.history != nil && s.editor == nil {
		_, _ = s.history.Write(line)
	}

	prog, err := s.parser.Parse(strings.NewReader(line+"\n"), "")
	if err == nil {
		err = cmp.Or(unsupported(prog), noJobControl(prog), noExitOptions(prog))
	}
	if err != nil {
		fmt.Fprintln(s.console.Err, errorStyle.Render(err.Error()))
		return statusNotRun, false, err
	}

	status, exited = s.runLine(ctx, prog)
	return status, exited, nil
}

// runLine runs a line statement by statement, and reports whether the session
// is to end, with the status the line asked for
func (s *session) runLine(ctx context.Context, prog *syntax.File) (status int, exited bool) {
	// Only what was typed before the line is stale; a ^C between two of its
	// statements is this line's to answer, and the next statement answers it.
	for drained := false; !drained; {
		select {
		case <-s.interrupts:
		default:
			drained = true
		}
	}

	for _, stmt := range prog.Stmts {
		s.exiting.Store(false)

		var interrupted bool
		var err error
		status, interrupted, err = s.runStmt(ctx, stmt)
		if ctx.Err() != nil {
			return 0, false
		}
		if err != nil {
			fmt.Fprintln(s.console.Err, errorStyle.Render(err.Error()))
		}
		if interrupted {
			// The terminal echoed the ^C where the output stopped
			fmt.Fprintln(s.console.Out)
			return status, false
		}
		if s.runner.Exited() && s.exiting.Load() {
			return status, true
		}
	}
	return status, false
}

// noExitOptions refuses set -e and set -u at the prompt, where to the
// interpreter they would end the shell at the next failure
func noExitOptions(prog *syntax.File) error {
	var err error
	syntax.Walk(prog, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 || literal(call.Args[0]) != "set" {
			return err == nil
		}
		for i := 1; i < len(call.Args); i++ {
			arg := literal(call.Args[i])
			if arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-") {
				break
			}
			flags := arg[1:]
			if strings.HasSuffix(flags, "o") && i+1 < len(call.Args) {
				i++
				if name := literal(call.Args[i]); name == "errexit" || name == "nounset" {
					err = fmt.Errorf("%s: set -o %s is not supported at the prompt, it would end the session at the next failure", call.Pos(), name)
					return false
				}
			}
			if strings.ContainsAny(flags, "eu") {
				err = fmt.Errorf("%s: set %s is not supported at the prompt, it would end the session at the next failure", call.Pos(), arg)
				return false
			}
		}
		return err == nil
	})
	return err
}

// literal is a word's text when it is one plain literal, and "" otherwise.
func literal(word *syntax.Word) string {
	if len(word.Parts) != 1 {
		return ""
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok {
		return ""
	}
	return lit.Value
}

// noJobControl refuses what the prompt cannot run: a statement sent to the
// background outlives the context the line runs on
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
func (s *session) runStmt(ctx context.Context, stmt *syntax.Stmt) (status int, interrupted bool, err error) {
	stmtCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		hit  atomic.Bool
		wg   sync.WaitGroup
		done = make(chan struct{})
	)

	// A ^C from earlier in the line, when no statement was watching for one.
	select {
	case <-s.interrupts:
		hit.Store(true)
		cancel()
	default:
	}

	stop := sync.OnceFunc(func() {
		close(done)
		wg.Wait()
	})
	defer stop()

	wg.Go(func() {
		for {
			select {
			case <-s.interrupts:
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

// paint brings styled text down to the caller's colour profile, which what
// readline paints itself does not go through
func (s *session) paint(styled string) string {
	if s.profile == colorprofile.TrueColor {
		return styled
	}
	var buf strings.Builder
	_, _ = (&colorprofile.Writer{Forward: &buf, Profile: s.profile}).Write([]byte(styled))
	return buf.String()
}

// profileOf is the colour profile of the caller's stdout
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

// captureInterrupts points the process' SIGINT at the session for as long as
// it holds the prompt.
func (s *session) captureInterrupts() (stop func()) {
	restore := func() {}
	if s.cfg.SuspendSignals != nil {
		restore = s.cfg.SuspendSignals(syscall.SIGINT)
	}

	signal.Notify(s.interrupts, syscall.SIGINT)

	return func() {
		signal.Stop(s.interrupts)
		restore()
	}
}

// isProcessStdio reports whether in and out are the process' own stdin and
// stdout, the only streams readline can edit on
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

// call notes an exit for the session to follow, then keeps lookups remote.
func (s *session) call(ctx context.Context, args []string) ([]string, error) {
	if args[0] == "exit" {
		s.exiting.Store(true)
	}
	return keepLookupsRemote(ctx, args)
}

// remoteBuiltins are the builtins the interpreter knows the names of but does
// not implement
var remoteBuiltins = map[string]bool{
	"bg": true, "bind": true, "caller": true, "compgen": true, "complete": true,
	"compopt": true, "disown": true, "enable": true, "fc": true, "fg": true,
	"history": true, "jobs": true, "kill": true, "logout": true, "newgrp": true,
	"suspend": true, "ulimit": true, "umask": true,
}

// keepLookupsRemote stops type, command -v and pwd -P from running locally, and
// sends the instance what the interpreter would otherwise refuse as a builtin.
func keepLookupsRemote(_ context.Context, args []string) ([]string, error) {
	var local bool
	switch args[0] {
	case "type":
		local = true
	case "command":
		// command <name> runs the interpreter's builtin directly, past this handler.
		local = len(args) > 1 && (args[1] == "-v" || args[1] == "-V" || remoteBuiltins[args[1]])
	case "pwd":
		local = slices.Contains(args[1:], "-P")
	default:
		local = remoteBuiltins[args[0]]
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
	for record := range strings.SplitSeq(out, "\x00") {
		if name, value, ok := strings.Cut(record, "="); ok && isEnvName(name) {
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

// lockWriter serialises writes to w from commands running at the same time
func lockWriter(mu *sync.Mutex, w io.Writer) io.Writer {
	locked := &lockedWriter{mu: mu, w: w}
	if f, ok := xio.Unwrap(w).(descriptor); ok {
		return &lockedFile{lockedWriter: locked, file: f}
	}
	return locked
}

type descriptor interface{ Fd() uintptr }

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type lockedFile struct {
	*lockedWriter
	file descriptor
}

func (l *lockedFile) Fd() uintptr { return l.file.Fd() }
