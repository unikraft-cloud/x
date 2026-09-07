// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/interp"
)

func TestExitStatus(t *testing.T) {
	require.NoError(t, codeToExitStatus(0))

	for _, tt := range []struct {
		name string
		code int
		want interp.ExitStatus
	}{
		{"failure", 1, 1},
		{"signal", 130, 130},
		{"max", 255, 255},
		{"interrupted", -2, 130},
		{"terminated", -15, 143},
		{"killed", -9, 137},
		{"highest-signal", -maxSignal, 128 + maxSignal},
		{"not-a-signal", -maxSignal - 1, 1},
		{"out-of-range", 256, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var status interp.ExitStatus
			require.ErrorAs(t, codeToExitStatus(tt.code), &status)
			assert.Equal(t, tt.want, status)
		})
	}
}

func TestIsEnvName(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  bool
	}{
		{"upper", "PATH", true},
		{"underscore-lead", "_x", true},
		{"digits-after-first", "A1", true},
		{"empty", "", false},
		{"digit-first", "1A", false},
		{"dash", "A-B", false},
		{"space", "a b", false},
		{"equals", "=x", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isEnvName(tt.input))
		})
	}
}

func TestResolve(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{"relative", "app.log", "/var/log/app.log"},
		{"absolute", "/etc/hosts", "/etc/hosts"},
		{"dot", ".", "/var/log"},
		{"parent", "../lib", "/var/lib"},
		{"nested", "a/b/../c", "/var/log/a/c"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolve("/var/log", tt.input))
		})
	}
}

func TestReadDirEntries(t *testing.T) {
	s := &session{
		runner: &interp.Runner{Dir: "/"},
		cfg:    Config{Transport: scriptTransport("ok\nd bin\x00d etc\x00f README\x00f a\nb\x00")},
	}

	entries, err := s.readDir(t.Context(), "/", "/")
	require.NoError(t, err)
	require.Len(t, entries, 4)

	assert.Equal(t, "README", entries[0].Name())
	assert.False(t, entries[0].IsDir())
	assert.Equal(t, "a\nb", entries[1].Name(), "a newline in a name survives the trip")
	assert.Equal(t, "bin", entries[2].Name())
	assert.True(t, entries[2].IsDir())
}

// scriptTransport answers every command with fixed output. Only the scripts in
// scripts/ go through it.
type scriptTransport string

func (t scriptTransport) Exec(_ context.Context, streams Streams, _ string, _ map[string]string, _ []string) (int, error) {
	fmt.Fprint(streams.Out, string(t))
	return 0, nil
}

// localTransport stands in for an instance by running commands here. The shell
// never learns the difference, so the routing, the remote filesystem handlers
// and the session state can all be exercised without a network.
type localTransport struct{}

func (localTransport) Exec(ctx context.Context, streams Streams, dir string, env map[string]string, args []string) (int, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, sortedEnv(env)...)
	cmd.Stdout, cmd.Stderr = streams.Out, streams.Err

	// Stdin goes through a pipe the command's exit closes, as [Transport] asks:
	// handed the reader itself, Wait would hold out for its EOF, which for the
	// terminal only comes once the shell has this command's exit in hand.
	if streams.In != nil {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return 0, err
		}
		defer stdin.Close()
		go func() {
			_, _ = io.Copy(stdin, streams.In)
			_ = stdin.Close()
		}()
	}

	var exitErr *exec.ExitError
	switch err := cmd.Run(); {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), nil
	default:
		return 0, err
	}
}

func sortedEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// echoBuiltins answer ":say <text>" by printing it, which is enough to see
// where a builtin's output ends up.
var echoBuiltins = map[string]Builtin{
	"say": BuiltinFunc(func(_ context.Context, streams Streams, args []string) (int, error) {
		fmt.Fprintln(streams.Out, strings.Join(args[1:], " "))
		return 0, nil
	}),
}

// builtinsNamed advertise names without doing anything under them.
func builtinsNamed(names ...string) map[string]Builtin {
	builtins := map[string]Builtin{}
	for _, name := range names {
		builtins[name] = BuiltinFunc(func(_ context.Context, _ Streams, args []string) (int, error) {
			return 0, fmt.Errorf("%s is not implemented", args[0])
		})
	}
	return builtins
}

// captured collects a session's output. A pipeline runs its commands at the
// same time and both write here, so the buffer has to be guarded — a real
// terminal is a file descriptor and does its own serialising.
type captured struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captured) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *captured) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ansi.Strip(c.buf.String())
}

func (c *captured) Len() int { return len(c.String()) }

func (c *captured) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Reset()
}

func newFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var", "log"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "var", "log", "app.log"), []byte("a\nb\nerror\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "var", "log", "boot.log"), []byte("boot\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hostname"), []byte("fakebox\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join("var", "log", "app.log"), filepath.Join(root, "current")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644))
	return root
}

func runLine(t *testing.T, root, line string) string {
	t.Helper()

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Command:   line,
		Transport: localTransport{},
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	require.NoError(t, err, "output: %s", out.String())

	return out.String()
}

func TestSession(t *testing.T) {
	root := newFixture(t)

	for _, tt := range []struct {
		name string
		line string
		want string
	}{
		{"echo", `echo hello`, "hello\n"},
		{"variables", `x=5; echo $((x * 2))`, "10\n"},
		{"control-flow", `for i in 1 2 3; do printf %s $i; done; echo`, "123\n"},
		{"command-substitution", `echo "[$(echo inner)]"`, "[inner]\n"},

		{"exit-status", `false; echo $?`, "1\n"},
		{"and-or", `true && echo yes || echo no`, "yes\n"},
		{"short-circuit", `false && echo unreachable; echo after`, "after\n"},

		{"cd-persists", `cd $R/var/log && pwd`, "$R/var/log\n"},
		{"pwd-var", `cd $R/var/log; echo $PWD`, "$R/var/log\n"},
		{"cd-dash", `cd $R/var/log; cd -; pwd`, "$R\n$R\n"},
		{"glob", `cd $R/var/log; echo *.log`, "app.log boot.log\n"},
		{"glob-onto-a-file", `cd $R; echo var/*/app.log`, "var/log/app.log\n"},
		{"glob-reads-through-it", `cd $R; cat var/*/boot.log`, "boot\n"},
		{"test-file", `[ -f $R/var/log/app.log ] && echo found`, "found\n"},
		{"test-dir", `[ -d $R/var/log ] && echo dir`, "dir\n"},

		{"subshell-cd-moves-the-subshell", `(cd $R/var/log; pwd)`, "$R/var/log\n"},
		{"subshell-cd-is-contained", `(cd $R/var/log); pwd`, "$R\n"},
		{"subshell-cd-globs-there", `(cd $R/var/log && echo *.log)`, "app.log boot.log\n"},
		{"substitution-cd-is-contained", `echo "[$(cd $R/var/log; pwd)]"; pwd`, "[$R/var/log]\n$R\n"},

		{"test-symlink", `[ -L $R/current ] && echo link`, "link\n"},
		{"test-symlink-follows", `[ -f $R/current ] && echo file`, "file\n"},
		{"test-fifo", `[ -p $R/pipe ] && echo fifo`, "fifo\n"},
		{"test-fifo-is-not-a-file", `[ -f $R/pipe ] || echo not-a-file`, "not-a-file\n"},
		{"test-executable", `[ -x $R/var/log ] && echo executable`, "executable\n"},
		{"test-not-executable", `[ -x $R/hostname ] || echo not-executable`, "not-executable\n"},
		{"owner-test-answers-rather-than-crashing", `[ -O $R/hostname ] || echo unowned`, "unowned\n"},

		{"pipeline", `cat $R/var/log/app.log | grep -c error`, "1\n"},
		{"null-command", `: ; echo $?`, "0\n"},

		{"discard", `echo hidden > /dev/null; echo shown`, "shown\n"},
		{"stderr-passes-through", `echo oops >&2`, "oops\n"},
		{"stderr-is-separable", `sh -c 'echo e >&2' 2>/dev/null; echo done`, "done\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line := strings.ReplaceAll(tt.line, "$R", root)
			want := strings.ReplaceAll(tt.want, "$R", root)
			assert.Equal(t, want, runLine(t, root, line))
		})
	}
}

func TestSessionHistory(t *testing.T) {
	s := &session{
		cfg:    Config{Builtins: builtinsNamed("start")},
		editor: &prompt{history: &sessionHistory{}},
	}
	for _, line := range []string{"echo one", "echo two", "echo two", "  "} {
		_, err := s.editor.history.Write(line)
		require.NoError(t, err)
	}

	var out captured
	require.NoError(t, s.runSessionBuiltin(Streams{Out: &out, Err: &out}, []string{"history"}))
	assert.Equal(t, "    1  echo one\n    2  echo two\n", out.String())

	assert.Equal(t, []string{"history", "start"}, s.builtinNames())

	bare := &session{}
	out.Reset()
	err := bare.runSessionBuiltin(Streams{Out: &out, Err: &out}, []string{"history"})
	assert.Equal(t, interp.ExitStatus(1), err, "no prompt, no history to show")
	assert.Contains(t, out.String(), "only at the prompt")
}

// chattyTransport writes straight to the streams, as the remote log poller
// does once a command starts producing output.
type chattyTransport struct{}

func (chattyTransport) Exec(_ context.Context, streams Streams, _ string, _ map[string]string, args []string) (int, error) {
	for i := range 50 {
		fmt.Fprintf(streams.Out, "%s-out-%d\n", args[0], i)
		fmt.Fprintf(streams.Err, "%s-err-%d\n", args[0], i)
	}
	return 0, nil
}

func TestConcurrentCommandsShareTheTerminal(t *testing.T) {
	var out bytes.Buffer

	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       "/",
		Command:   "alpha | beta",
		Transport: chattyTransport{},
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	require.NoError(t, err)

	assert.Contains(t, out.String(), "beta-out-49")
}

func TestBuiltinsCompose(t *testing.T) {
	root := newFixture(t)
	out := filepath.Join(root, "said.txt")

	run := func(t *testing.T, line string) string {
		t.Helper()

		var buf captured
		_, err := Run(t.Context(), Config{
			Instance:  "fake",
			Dir:       root,
			Command:   line,
			Transport: localTransport{},
			Builtins:  echoBuiltins,
		}, Streams{In: strings.NewReader(""), Out: &buf, Err: &buf})
		require.NoError(t, err)

		return buf.String()
	}

	assert.Equal(t, "HELLO\n", run(t, ":say hello | tr a-z A-Z"), "piped into a command on the instance")
	assert.Equal(t, "[x]\n", run(t, `echo "[$(:say x)]"`), "captured by a substitution")
	assert.Empty(t, run(t, ":say quiet > "+out), "redirected away")

	written, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "quiet\n", string(written))
}

func TestRedirectionWritesToTheInstance(t *testing.T) {
	root := newFixture(t)
	out := filepath.Join(root, "out.txt")

	runLine(t, root, "echo written > "+out)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "written\n", string(data))

	runLine(t, root, "echo more >> "+out)
	data, err = os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "written\nmore\n", string(data))

	assert.Equal(t, "written\nmore\n", runLine(t, root, "cat < "+out))

	runLine(t, root, "printf '' > "+out)
	data, err = os.ReadFile(out)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestEnvironment(t *testing.T) {
	root := newFixture(t)

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Env:       map[string]string{"GREETING": "hi"},
		Command:   `echo "$GREETING $PWD"; env | grep -c '^PATH='`,
		Transport: localTransport{},
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	require.NoError(t, err)

	assert.Equal(t, "hi "+root+"\n1\n", out.String())
}

// deadlineTransport reports whether the shell bounded the command it was
// given.
type deadlineTransport struct{ deadline bool }

func (t *deadlineTransport) Exec(ctx context.Context, _ Streams, _ string, _ map[string]string, _ []string) (int, error) {
	_, t.deadline = ctx.Deadline()
	return 0, nil
}

func TestEnvironProbeIsBounded(t *testing.T) {
	probe := &deadlineTransport{}
	s := &session{cfg: Config{Transport: probe, Dir: "/"}}

	_, err := s.environ(t.Context())

	require.NoError(t, err)
	assert.True(t, probe.deadline,
		"an instance slow to answer must not hold the prompt back")
}

// unreachableTransport is an instance that cannot be reached at all, as
// opposed to one that runs a command and reports a failure.
type unreachableTransport struct{ calls int }

func (t *unreachableTransport) Exec(context.Context, Streams, string, map[string]string, []string) (int, error) {
	t.calls++
	return 0, errors.New("504 Gateway Time-out")
}

func TestUnreachableInstanceIsNotAPrompt(t *testing.T) {
	probe := &unreachableTransport{}

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "sandbox",
		Dir:       "/",
		Command:   "echo unreachable",
		Transport: probe,
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not reach sandbox")
	assert.Contains(t, err.Error(), "504 Gateway Time-out")
	assert.Equal(t, instanceProbeAttempts, probe.calls, "asked again, then gave up")
	assert.Empty(t, out.String(), "nothing ran")
}

func TestAnInstanceWithoutAShellStillOpens(t *testing.T) {
	s := &session{cfg: Config{
		Instance:  "quiet",
		Transport: scriptTransport(""),
		Dir:       "/",
	}}

	env, err := s.environ(t.Context())

	require.NoError(t, err, "a command that reports nothing is not a failure")
	assert.Equal(t, []string{
		"EUID=0", "GID=0", "HOME=/",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"UID=0",
	}, env, "filled in here, so the interpreter cannot answer with this machine's")
}

func TestEnvironIsTheInstances(t *testing.T) {
	s := &session{cfg: Config{
		Transport: scriptTransport("HOME=/instance\nPATH=/bin\nUID=7\nEUID=7\nGID=7\n"),
		Dir:       "/srv",
		Env:       map[string]string{"EXTRA": "1"},
	}}

	env, err := s.environ(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{
		"EUID=7", "EXTRA=1", "GID=7", "HOME=/instance", "PATH=/bin", "UID=7",
	}, env)
}

func TestShellNeedsATerminal(t *testing.T) {
	root := newFixture(t)

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Transport: localTransport{},
	}, Streams{
		In:  strings.NewReader("cd var/log\nls\n"),
		Out: &out,
		Err: &out,
	})
	require.ErrorIs(t, err, errNotATerminal)

	assert.Empty(t, out.String())
}

func TestAnUnknownBuiltinIsNamed(t *testing.T) {
	root := newFixture(t)

	for _, tt := range []struct {
		name     string
		builtins map[string]Builtin
		want     string
	}{
		{"with-help-to-point-at", builtinsNamed("help"), `unknown builtin "frobnicate"; try :help`},
		{"without", nil, `unknown builtin "frobnicate"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out captured
			code, err := Run(t.Context(), Config{
				Instance:  "fake",
				Dir:       root,
				Command:   ":frobnicate",
				Transport: localTransport{},
				Builtins:  tt.builtins,
			}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
			require.NoError(t, err)

			assert.Equal(t, statusBuiltinNotFound, code)
			assert.Equal(t, tt.want+"\n", ansi.Strip(out.String()))
		})
	}
}

func TestSessionBuiltinNamesAreReserved(t *testing.T) {
	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Transport: localTransport{},
		Builtins:  builtinsNamed("start", "history"),
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	require.ErrorContains(t, err, `"history"`)

	assert.Empty(t, out.String())
}

func TestSessionBuiltinsOutsideThePrompt(t *testing.T) {
	root := newFixture(t)

	for _, tt := range []struct {
		name, line string
		code       int
		want       string
	}{
		{"history-needs-the-prompt", ":history", 1, "history: only at the prompt\n"},
		{"help-lists-the-session-s-own", ":help", 0, "  :history"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out captured
			code, err := Run(t.Context(), Config{
				Instance:  "fake",
				Dir:       root,
				Command:   tt.line,
				Transport: localTransport{},
			}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
			require.NoError(t, err)

			assert.Equal(t, tt.code, code)
			assert.Contains(t, ansi.Strip(out.String()), tt.want)
		})
	}
}

func TestFailedCommandIsNotAShellFailure(t *testing.T) {
	root := newFixture(t)

	var out captured
	code, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Command:   "false",
		Transport: localTransport{},
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})

	require.NoError(t, err, "a failing command is not the shell failing")
	assert.Equal(t, 1, code, "but its status is the session's to report")
}

// exitTransport reports a fixed outcome for every command.
type exitTransport struct {
	code int
	err  error
}

func (t exitTransport) Exec(context.Context, Streams, string, map[string]string, []string) (int, error) {
	return t.code, t.err
}

func TestScriptErrors(t *testing.T) {
	t.Run("non-zero-exit-is-a-missing-path", func(t *testing.T) {
		s := &session{runner: &interp.Runner{Dir: "/"}, cfg: Config{Transport: exitTransport{code: 1}}}

		_, err := s.stat(t.Context(), "/", "/nope", true)
		assert.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("an-unreachable-instance-says-so", func(t *testing.T) {
		boom := errors.New("504 Gateway Time-out")
		s := &session{runner: &interp.Runner{Dir: "/"}, cfg: Config{Transport: exitTransport{err: boom}}}

		_, err := s.readDir(t.Context(), "/", "/")
		require.ErrorIs(t, err, boom)
		assert.NotErrorIs(t, err, fs.ErrNotExist)
	})
}

func TestAccessSeparatesMissingFromDenied(t *testing.T) {
	root := newFixture(t)
	s := &session{runner: &interp.Runner{Dir: root}, cfg: Config{Transport: localTransport{}}}

	require.NoError(t, s.access(t.Context(), root, "hostname", interp.AccessRead))

	err := s.access(t.Context(), root, "nope", interp.AccessRead)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.NotErrorIs(t, err, fs.ErrPermission)

	if os.Geteuid() == 0 {
		t.Skip("root may read anything")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "sealed"), []byte("x"), 0o000))

	err = s.access(t.Context(), root, "sealed", interp.AccessRead)
	require.ErrorIs(t, err, fs.ErrPermission)
	assert.NotErrorIs(t, err, fs.ErrNotExist)
}

func TestReadDirAnswersForSymlinkedDirectories(t *testing.T) {
	root := newFixture(t)
	require.NoError(t, os.Symlink(filepath.Join(root, "var", "log"), filepath.Join(root, "logs")))

	s := &session{runner: &interp.Runner{Dir: root}, cfg: Config{Transport: localTransport{}}}

	entries, err := s.readDir(t.Context(), root, ".")
	require.NoError(t, err)

	byName := map[string]fs.DirEntry{}
	for _, e := range entries {
		byName[e.Name()] = e
	}
	require.Contains(t, byName, "logs")
	require.Contains(t, byName, "current")
	assert.True(t, byName["logs"].IsDir())
	assert.False(t, byName["current"].IsDir())
}

func TestReadDirTellsAFileFromAMissingPath(t *testing.T) {
	root := newFixture(t)
	s := &session{runner: &interp.Runner{Dir: root}, cfg: Config{Transport: localTransport{}}}

	_, err := s.readDir(t.Context(), root, "hostname")
	require.ErrorIs(t, err, syscall.ENOTDIR)
	require.NotErrorIs(t, err, fs.ErrNotExist)

	_, err = s.readDir(t.Context(), root, "nope")
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestGlobMatchesASymlinkedDirectory(t *testing.T) {
	root := newFixture(t)
	require.NoError(t, os.Symlink(filepath.Join(root, "var", "log"), filepath.Join(root, "logs")))

	assert.Equal(t, "logs/ var/\n", runLine(t, root, "echo */"))
}

func TestAFailedRedirectionStopsTheCommand(t *testing.T) {
	root := newFixture(t)
	marker := filepath.Join(root, "marker")

	out := runLine(t, root, "touch "+marker+" > "+filepath.Join(root, "nope", "out.txt")+"; echo status=$?")

	assert.Contains(t, out, "out.txt", "the interpreter looks at neither Write nor Close")
	assert.Contains(t, out, "status=1")
	assert.NoFileExists(t, marker, "the command never ran, the way a real shell would not run it")
}

func TestASignalledHelperIsAFailureUnlessInterrupted(t *testing.T) {
	s := &session{cfg: Config{Transport: exitTransport{code: -int(syscall.SIGKILL)}}}

	err := s.redirect(t.Context(), "write", "/tmp/out", writeScript, Streams{})
	require.ErrorContains(t, err, "signalled (9)",
		"a helper killed under a live statement lost data; that cannot look like success")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	assert.NoError(t, s.redirect(ctx, "write", "/tmp/out", writeScript, Streams{}),
		"a helper killed because the statement was interrupted is not the redirection failing")
}

func TestASingleCommandLineSaysNothingExtra(t *testing.T) {
	root := newFixture(t)

	assert.Equal(t, "hello\n", runLine(t, root, "echo hello"),
		"the banner is for a person at a prompt, not for a script")
}

func TestATruncationHappensBeforeTheCommandRuns(t *testing.T) {
	root := newFixture(t)
	boot := filepath.Join(root, "var", "log", "boot.log")

	runLine(t, root, "cat "+boot+" > "+boot)

	data, err := os.ReadFile(boot)
	require.NoError(t, err)
	assert.Empty(t, data, "the file is opened once, before the command reads it, as a real shell does")
}

func TestAStreamCanBeRedirectedFrom(t *testing.T) {
	root := newFixture(t)
	fifo := filepath.Join(root, "pipe")

	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		_, _ = w.WriteString("first line\n")
		time.Sleep(30 * time.Second)
	}()

	done := make(chan string, 1)
	go func() {
		var buf captured
		_, _ = Run(t.Context(), Config{
			Instance:  "fake",
			Dir:       root,
			Command:   "head -1 < " + fifo,
			Transport: localTransport{},
		}, Streams{In: strings.NewReader(""), Out: &buf, Err: &buf})
		done <- buf.String()
	}()

	select {
	case got := <-done:
		assert.Equal(t, "first line\n", got)
	case <-time.After(20 * time.Second):
		t.Fatal("the open waited for a full buffer instead of the first write")
	}
}

func TestARedirectionStreams(t *testing.T) {
	root := newFixture(t)
	big := filepath.Join(root, "big.txt")

	written := runLine(t, root, "yes 0123456789 | head -c 4000000 > "+big)
	assert.Empty(t, written)

	info, err := os.Stat(big)
	require.NoError(t, err)
	assert.Equal(t, int64(4000000), info.Size(), "nothing was held back for a buffer to fit")

	assert.Equal(t, "4000000\n", runLine(t, root, "wc -c < "+big))
}

func TestAMissingFileCannotBeRedirectedFrom(t *testing.T) {
	root := newFixture(t)

	_, err := (&session{cfg: Config{Transport: localTransport{}}}).openRead(
		t.Context(), filepath.Join(root, "nope"), io.Discard)

	require.Error(t, err, "the open reports it, rather than the reader failing later")
}

func TestFileTestsAskTheInstance(t *testing.T) {
	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       "/",
		Command:   `[ -r /etc/hosts ] || echo no-r; [ -w /tmp ] || echo no-w; [ -x /bin/sh ] || echo no-x`,
		Transport: exitTransport{code: 1},
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	require.NoError(t, err)

	assert.Equal(t, "no-r\nno-w\nno-x\n", out.String())
}

func TestCaptureInterruptsHandsSIGINTBack(t *testing.T) {
	var (
		suspended []os.Signal
		restored  bool
	)
	suspend := func(sig ...os.Signal) func() {
		suspended = sig
		return func() { restored = true }
	}

	sigint, release := captureInterrupts(suspend)

	// Registered after captureInterrupts so that a regression to signal.Reset
	// cannot leave SIGINT unhandled and kill this binary.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	assert.Equal(t, []os.Signal{syscall.SIGINT}, suspended)

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	select {
	case <-sigint:
	case <-time.After(2 * time.Second):
		t.Fatal("the interrupt never reached the shell")
	}

	release()
	assert.True(t, restored, "the signal was never handed back")
}

func TestCaptureInterruptsWithoutASuspend(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	_, release := captureInterrupts(nil)
	assert.NotPanics(t, release)
}

func TestStdinReachesTheCommand(t *testing.T) {
	root := newFixture(t)

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Command:   "cat",
		Transport: localTransport{},
	}, Streams{In: strings.NewReader("payload\n"), Out: &out, Err: &out})
	require.NoError(t, err)

	assert.Equal(t, "payload\n", out.String())
}

func TestTheInterpreterReadsTheTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })

	go func() { _, _ = ptmx.WriteString("hello\n") }()

	var out captured
	_, err = Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Command:   "read x; echo got:$x",
		Transport: localTransport{},
	}, Streams{In: tty, Out: &out, Err: &out})
	require.NoError(t, err)

	assert.Equal(t, "got:hello\n", out.String(), "read is a builtin, so it is the interpreter's stdin that must be the terminal")
}

func TestARemoteCommandGivesTheTerminalBack(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })

	root := newFixture(t)

	var out captured
	returned := make(chan error, 1)
	go func() {
		_, err := Run(t.Context(), Config{
			Instance:  "fake",
			Dir:       root,
			Command:   "/bin/echo remote-ok",
			Transport: localTransport{},
		}, Streams{In: tty, Out: &out, Err: &out})
		returned <- err
	}()

	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the command exited but the session waited for the terminal to run dry")
	}
	assert.Equal(t, "remote-ok\n", out.String())
}

func TestARedirectionDoesNotLeakDescriptors(t *testing.T) {
	root := newFixture(t)

	open := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip("no /proc to count descriptors with:", err)
		}
		return len(entries)
	}

	before := open()
	runLine(t, root, `for i in $(seq 200); do head -c 1 < var/log/app.log > /dev/null; done`)
	assert.LessOrEqual(t, open(), before+4, "every < file must hand its descriptor back")
}

func TestTheInstanceAnswersWhatACommandIs(t *testing.T) {
	root := newFixture(t)
	require.NoError(t, os.Symlink("var", filepath.Join(root, "physical")))

	assert.Contains(t, runLine(t, root, `type sh`), "sh is ", "type asks the instance's PATH")
	assert.Contains(t, runLine(t, root, `command -v sh`), "/sh\n", "so does command -v")
	assert.Equal(t, filepath.Join(root, "var")+"\n", runLine(t, root, `cd physical && pwd -P`),
		"pwd -P resolves the instance's links, not this machine's")
}

func TestWhatOnlyThisMachineCouldAnswerIsRefused(t *testing.T) {
	root := newFixture(t)

	for _, tt := range []struct{ name, line, want string }{
		{"process-substitution", `cat <(echo hi)`, "process substitution is not supported"},
		{"user-tilde", `echo ~root`, "~user is not supported"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out captured
			_, err := Run(t.Context(), Config{
				Instance:  "fake",
				Dir:       root,
				Command:   tt.line,
				Transport: localTransport{},
			}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
			require.ErrorContains(t, err, tt.want)
			assert.Empty(t, out.String(), "refused before anything ran")
		})
	}

	assert.Equal(t, "home\n", runLine(t, root, `HOME=home; echo ~`), "a bare ~ is the instance's HOME, and fine")
}

func TestNewerThanAsksTheInstance(t *testing.T) {
	root := newFixture(t)
	older, newer := filepath.Join(root, "var", "log", "boot.log"), filepath.Join(root, "var", "log", "app.log")
	then := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(older, then, then))

	assert.Equal(t, "newer\n", runLine(t, root, `[ var/log/app.log -nt var/log/boot.log ] && echo newer || echo older`))
	assert.Equal(t, "older\n", runLine(t, root, `[ var/log/boot.log -nt var/log/app.log ] && echo newer || echo older`))
	_ = newer
}

// noShellTransport is an instance with no sh: commands run by argv, the helpers do not.
type noShellTransport struct{ localTransport }

func (t noShellTransport) Exec(ctx context.Context, streams Streams, dir string, env map[string]string, args []string) (int, error) {
	if args[0] == "sh" {
		return statusBuiltinNotFound, nil
	}
	return t.localTransport.Exec(ctx, streams, dir, env, args)
}

func TestAnInstanceWithoutAShellSaysSoOnFiles(t *testing.T) {
	root := newFixture(t)

	var out captured
	code, err := Run(t.Context(), Config{
		Instance:  "bare",
		Dir:       root,
		Command:   "echo runs; cat < var/log/app.log",
		Transport: noShellTransport{},
	}, Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	require.NoError(t, err, "the session opens, commands run by argv")

	assert.Equal(t, 1, code)
	assert.Contains(t, out.String(), "runs\n")
	assert.Contains(t, out.String(), errNoShell.Error(), "but a redirection says what it is missing, not that the file is")
}

// complainingTransport fails every helper with a word about why.
type complainingTransport struct{}

func (complainingTransport) Exec(_ context.Context, streams Streams, _ string, _ map[string]string, _ []string) (int, error) {
	fmt.Fprintln(streams.Err, "sh: out of memory")
	return 2, nil
}

func TestAFailingProbeSaysWhy(t *testing.T) {
	s := &session{runner: &interp.Runner{Dir: "/"}, cfg: Config{Transport: complainingTransport{}}}

	_, err := s.stat(t.Context(), "/", "/etc", true)
	require.ErrorContains(t, err, "out of memory")
	assert.NotErrorIs(t, err, fs.ErrNotExist, "a probe that complained did not find the path missing")
}
