// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"unikraft.com/x/stdio"
)

const (
	instanceRestartTimeout = 90 * time.Second
	instanceRestartPoll    = 1 * time.Second

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
)

var (
	dirReset       = mustParse("cd /").Stmts[0]
	interruptReset = mustParse(fmt.Sprintf("(exit %d)", StatusInterrupted)).Stmts[0]

	sessionEnd = mustParse("")
)

// console is where the session writes, serialised so two commands cannot interleave a write.
type console struct {
	Out, Err io.Writer
}

type state struct {
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
	restarted   atomic.Bool
	euid        string

	statsMu  sync.Mutex
	stats    map[statKey]fs.FileInfo
	statsGen uint64
}

func (s *state) dir() string {
	return s.runner.Dir
}

func Run(ctx context.Context, cfg Config, streams stdio.Stdio) (int, error) {
	s, err := newState(ctx, cfg, streams)
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

// newState initializes the streams it writes on, the instance's environment,
// and the interpreter over both.
func newState(ctx context.Context, cfg Config, streams stdio.Stdio) (*state, error) {
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

	s := &state{cfg: cfg, parser: syntax.NewParser(), interrupts: make(chan os.Signal, 4)}
	if f, ok := streams.Stdin.(*os.File); ok && xio.IsTTYReader(f) {
		s.tty = f
	}
	s.stdin = cmp.Or(s.tty, cfg.Input)
	s.onTerminal = cfg.Command == "" && s.tty != nil && xio.IsTTY(streams.Stdout)
	s.interactive = s.onTerminal && xio.IsStdin(s.tty) && xio.IsStdout(streams.Stdout)
	s.profile = profileOf(streams.Stdout)

	var terminal sync.Mutex
	s.console = console{
		Out: lockWriter(&terminal, &colorprofile.Writer{Forward: streams.Stdout, Profile: s.profile}),
		Err: lockWriter(&terminal, &colorprofile.Writer{Forward: streams.Stderr, Profile: s.profile}),
	}

	env, err := s.environ(ctx)
	if err != nil {
		return nil, err
	}
	env, tmpdir, exported := withoutTmpdir(env)

	in := streams.Stdin
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
	if exported {
		if quoted, err := syntax.Quote(tmpdir, syntax.LangPOSIX); err == nil {
			_ = runner.Run(ctx, mustParse("export TMPDIR=" + quoted).Stmts[0])
		}
	}
	s.runner = runner
	return s, nil
}

// withoutTmpdir takes TMPDIR out of the environment the interpreter runs on,
// and says whether the instance exported one at all: an empty one is still one.
func withoutTmpdir(env []string) ([]string, string, bool) {
	var tmpdir string
	var exported bool
	kept := make([]string, 0, len(env))
	for _, record := range env {
		if name, value, ok := strings.Cut(record, "="); ok && name == "TMPDIR" {
			tmpdir, exported = value, true
			continue
		}
		kept = append(kept, record)
	}
	return kept, tmpdir, exported
}

func (s *state) runSource(ctx context.Context, src io.Reader) (int, error) {
	prog, err := syntax.NewParser().Parse(src, "")
	if err != nil {
		return 0, err
	}
	if err := unsupported(prog); err != nil {
		return 0, err
	}
	return interpStatus(s.runner.Run(ctx, prog))
}

// interpStatus separates a command's exit status from the interpreter failing.
func interpStatus(err error) (int, error) {
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
		case *syntax.Stmt:
			if n.Background || n.Coprocess {
				err = fmt.Errorf("%s: no job control, & is not supported", n.Pos())
			}
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

func (s *state) runInteractive(ctx context.Context) (int, error) {
	stop := s.captureInterrupts()
	defer stop()
	defer s.plainKeys()()

	if s.cfg.Banner != "" {
		fmt.Fprintln(s.console.Err, bannerStyle.Render(s.cfg.Banner))
	}

	s.history = &sessionHistory{}
	s.editor = newPrompt(promptConfig{
		history:   s.history,
		prompt:    s.prompt,
		paint:     s.paint,
		isBuiltin: s.isBuiltinName,
		complete:  s.completer(ctx),
	})

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
func (s *state) runInput(ctx context.Context, input string) (status int, exited bool, err error) {
	// readline records what it accepts itself, so only a driven session's lines
	// are on the session to keep.
	if s.history != nil && s.editor == nil {
		_, _ = s.history.Write(input)
	}

	prog, err := s.parser.Parse(strings.NewReader(input+"\n"), "")
	if err == nil {
		err = unsupported(prog)
	}
	if err != nil {
		fmt.Fprintln(s.console.Err, errorStyle.Render(err.Error()))
		return statusNotRun, false, err
	}

	status, exited = s.runFile(ctx, prog)
	return status, exited, nil
}

// runFile runs a parsed input statement by statement, and reports whether the
// session is to end, with the status it asked for
func (s *state) runFile(ctx context.Context, prog *syntax.File) (status int, exited bool) {
	defer func() { s.commands = nil }()
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
		if s.restarted.Swap(false) {
			s.relocate(ctx)
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

// runStmt runs one statement, watching for the interrupt that ends it early.
func (s *state) runStmt(ctx context.Context, stmt *syntax.Stmt) (status int, interrupted bool, err error) {
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

	status, err = interpStatus(s.runner.Run(stmtCtx, stmt))
	stop()

	if !hit.Load() {
		return status, false, err
	}
	s.clearInterrupt(ctx)
	return StatusInterrupted, true, nil
}

func (s *state) settle(ctx context.Context) bool {
	s.commands = nil

	if s.noShell {
		// Nothing to probe with: the next command finds out for itself.
		return true
	}

	fmt.Fprintln(s.console.Err, hintStyle.Render("waiting for instance restart"))

	waitCtx, cancel := context.WithTimeout(ctx, instanceRestartTimeout)
	defer cancel()

	for !s.cfg.Transport.Alive(waitCtx) {
		switch {
		case ctx.Err() != nil:
			fmt.Fprintln(s.console.Err, hintStyle.Render("aborting instance wait"))
			return false
		case waitCtx.Err() != nil:
			fmt.Fprintln(s.console.Err, errorStyle.Render(
				`the instance has not come back: try ":get" or reconnect`))
			return false
		}
		select {
		case <-time.After(instanceRestartPoll):
		case <-waitCtx.Done():
		}
	}
	return true
}

func (s *state) relocate(ctx context.Context) {
	if s.dir() == probeDir {
		return
	}
	if _, err := s.cfg.Transport.Stat(ctx, probeDir, s.dir(), true); err == nil {
		return
	}

	fmt.Fprintln(s.console.Err, hintStyle.Render(
		"the working directory did not survive the restart: moved to "+probeDir))
	_ = s.runner.Run(ctx, dirReset)
}

// answers is whether the instance runs a probe; script bounds the wait.
func mustParse(src string) *syntax.File {
	prog, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		panic(err)
	}
	return prog
}

func (s *state) clearInterrupt(ctx context.Context) {
	_ = s.runner.Run(ctx, interruptReset)
}

func (s *state) prompt(continuation bool) string {
	if continuation {
		return s.paint(continuationStyle.Render("> "))
	}
	return s.paint(promptStyle.Render(s.cfg.Instance) +
		promptDirStyle.Render(":"+s.dir()) +
		promptStyle.Render("$ "))
}

// paint brings styled text down to the caller's colour profile, which what
// readline paints itself does not go through
func (s *state) paint(styled string) string {
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

func (s *state) plainKeys() func() {
	fmt.Fprint(s.console.Out, ansi.DisableKittyKeyboard, ansi.ResetModifyOtherKeys)
	return func() {
		fmt.Fprint(s.console.Out, ansi.PopKittyKeyboard(1), ansi.ResetModifyOtherKeys)
	}
}

// captureInterrupts points the process' SIGINT at the session for as long as
// it holds the prompt.
func (s *state) captureInterrupts() (stop func()) {
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

// call notes an exit, vets what the interpreter would parse again, runs a
// session builtin typed bare, and keeps lookups remote.
func (s *state) call(ctx context.Context, args []string) ([]string, error) {
	if args[0] == "exit" {
		s.exiting.Store(true)
	}
	if err := vetReparsed(reparsing(args)); err != nil {
		return nil, err
	}
	if args[0] == helpBuiltin || slices.Contains(sessionBuiltinNames, args[0]) {
		return append([]string{BuiltinMarker + args[0]}, args[1:]...), nil
	}
	return keepLookupsRemote(ctx, args)
}

func vetReparsed(args []string) error {
	switch args[0] {
	case "eval":
		if err := vetSource(strings.Join(args[1:], " ")); err != nil {
			return fmt.Errorf("eval: %w", err)
		}
	case "trap":
		if action, installed := trapAction(args[1:]); installed {
			if err := vetSource(action); err != nil {
				return fmt.Errorf("trap: %w", err)
			}
		}
	case "source", ".":
		return fmt.Errorf("%s is not supported, the file would be read here and run unchecked", args[0])
	}
	return nil
}

// trapAction is what the interpreter would install, past the options it reads
// first: one argument left names a signal to restore, and none lists the traps.
func trapAction(args []string) (string, bool) {
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		if args[0] == "" || (args[0][0] != '-' && args[0][0] != '+') {
			break
		}
		args = args[1:]
	}
	if len(args) < 2 {
		return "", false
	}
	return args[0], true
}

func reparsing(args []string) []string {
	for len(args) > 1 {
		switch args[0] {
		case "builtin", "command":
			args = args[1:]
			if args[0] == "--" && len(args) > 1 {
				args = args[1:]
			}
		default:
			return args
		}
		if strings.HasPrefix(args[0], "-") {
			return args
		}
	}
	return args
}

// vetSource is [unsupported] over text that has not been parsed yet
func vetSource(src string) error {
	prog, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		return err
	}
	return unsupported(prog)
}

// remoteBuiltins are the builtins the interpreter knows the names of but does
// not implement
var remoteBuiltins = map[string]bool{
	"bg":       true,
	"bind":     true,
	"caller":   true,
	"compgen":  true,
	"complete": true,
	"compopt":  true,
	"disown":   true,
	"enable":   true,
	"fc":       true,
	"fg":       true,
	"jobs":     true,
	"kill":     true,
	"logout":   true,
	"newgrp":   true,
	"suspend":  true,
	"ulimit":   true,
	"umask":    true,
}

// keepLookupsRemote stops type, command -v and pwd -P from running locally, and
// sends the instance what the interpreter would otherwise refuse as a builtin.
//
// HACK: the interpreter resolves these as its own builtins before the exec
// handler is reached, so there is nowhere later to route them from. Rewriting
// the line as "sh -c" makes them commands again, which the handler does see.
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

func (s *state) environ(ctx context.Context) ([]string, error) {
	exported, err := s.cfg.Transport.Environ(ctx)
	switch {
	case errors.Is(err, ErrNoShell):
		// Nothing to probe with, but the session still opens: the next command
		// finds out for itself.
		s.noShell = true
	case err != nil:
		log.G(ctx).Debug().Err(err).Msg("could not read the instance environment")

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("could not reach %s: %w", cmp.Or(s.cfg.Instance, "the instance"), err)
	}

	vars := map[string]string{}
	for _, record := range exported {
		if name, value, ok := strings.Cut(record, "="); ok {
			vars[name] = value
		}
	}
	s.euid = vars["EUID"]
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

func (s *state) builtinNames() []string {
	names := slices.AppendSeq(slices.Clone(sessionBuiltinNames), maps.Keys(s.cfg.Builtins))
	slices.Sort(names)
	return names
}
