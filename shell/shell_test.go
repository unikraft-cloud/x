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
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
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

	"unikraft.com/x/stdio"
)

func TestCodeToExitStatus(t *testing.T) {
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
	s := ExecTransport(scriptTransport("ok\nd bin\x00d etc\x00f README\x00f a\nb\x00").Exec)

	entries, err := s.ReadDir(t.Context(), "/", "/")
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
// local is the test's transport: commands run here, and everything else is
// asked of the instance's sh, as it is for any transport with no API of its own.
func local() ExecTransport { return LocalTransport() }

type scriptTransport string

func (t scriptTransport) Exec(_ context.Context, cmd Command) (int, error) {
	fmt.Fprint(cmd.Streams.Stdout, string(t))
	return 0, nil
}

// echoBuiltins answer ":say <text>" by printing it, which is enough to see
// where a builtin's output ends up.
var echoBuiltins = map[string]Builtin{
	"say": BuiltinFunc(func(_ context.Context, streams stdio.Stdio, args []string) (int, error) {
		fmt.Fprintln(streams.Stdout, strings.Join(args[1:], " "))
		return 0, nil
	}),
}

// builtinsNamed advertise names without doing anything under them.
func builtinsNamed(names ...string) map[string]Builtin {
	builtins := map[string]Builtin{}
	for _, name := range names {
		builtins[name] = BuiltinFunc(func(_ context.Context, _ stdio.Stdio, args []string) (int, error) {
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
		Transport: local(),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
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
		{"signalled-status", `sh -c 'kill -TERM $$'; echo $?`, "143\n"},
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

		{"exported-variables-travel", `export y=1; env | grep -c '^y='`, "1\n"},
		{"unexported-variables-stay", `x=secret; env | grep -c '^x='`, "0\n"},

		{"kill-is-the-instances", `kill -l 15`, "TERM\n"},
		{"command-kill-is-too", `command kill -l 9`, "KILL\n"},

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
	s := &state{
		cfg:     Config{Builtins: builtinsNamed("start")},
		history: &sessionHistory{},
	}
	for _, line := range []string{"echo one", "echo two", "echo two", "  "} {
		_, err := s.history.Write(line)
		require.NoError(t, err)
	}

	var out captured
	require.NoError(t, s.runSessionBuiltin(stdio.Stdio{Stdout: &out, Stderr: &out}, []string{"history"}))
	assert.Equal(t, "    1  echo one\n    2  echo two\n", out.String())

	assert.Equal(t, []string{"history", "start"}, s.builtinNames())

	bare := &state{}
	out.Reset()
	err := bare.runSessionBuiltin(stdio.Stdio{Stdout: &out, Stderr: &out}, []string{"history"})
	assert.Equal(t, interp.ExitStatus(1), err, "no prompt, no history to show")
	assert.Contains(t, out.String(), "only at the prompt")
}

// chattyTransport writes straight to the streams, as the remote log poller
// does once a command starts producing output.
type chattyTransport struct{}

func (chattyTransport) Exec(_ context.Context, cmd Command) (int, error) {
	for i := range 50 {
		fmt.Fprintf(cmd.Streams.Stdout, "%s-out-%d\n", cmd.Args[0], i)
		fmt.Fprintf(cmd.Streams.Stderr, "%s-err-%d\n", cmd.Args[0], i)
	}
	return 0, nil
}

func TestConcurrentCommandsShareTheTerminal(t *testing.T) {
	var out bytes.Buffer

	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       "/",
		Command:   "alpha | beta",
		Transport: ExecTransport(chattyTransport{}.Exec),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
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
			Transport: local(),
			Builtins:  echoBuiltins,
		}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &buf, Stderr: &buf})
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
		Transport: local(),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
	require.NoError(t, err)

	assert.Equal(t, "hi "+root+"\n1\n", out.String())
}

// deadlineTransport reports whether the shell bounded the command it was
// given.
type deadlineTransport struct{ deadline bool }

func (t *deadlineTransport) Exec(ctx context.Context, _ Command) (int, error) {
	_, t.deadline = ctx.Deadline()
	return 0, nil
}

func TestEnvironProbeIsBounded(t *testing.T) {
	probe := &deadlineTransport{}
	s := &state{cfg: Config{Transport: ExecTransport(probe.Exec), Dir: "/"}}

	_, err := s.environ(t.Context())

	require.NoError(t, err)
	assert.True(t, probe.deadline,
		"an instance slow to answer must not hold the prompt back")
}

func TestEveryProbeIsBounded(t *testing.T) {
	probe := &deadlineTransport{}
	s := ExecTransport(probe.Exec)

	_, _ = s.Environ(t.Context())

	assert.True(t, probe.deadline,
		"a probe outside a statement has no ^C to fall back on")
}

// slowStatTransport is an instance that answers what is at a path more slowly
// than a probe is given to wait.
type slowStatTransport struct{}

func (t slowStatTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	if len(cmd.Args) > 2 && cmd.Args[2] == statScript {
		select {
		case <-time.After(instanceProbeTimeout + 200*time.Millisecond):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return local().Exec(ctx, cmd)
}

func TestASlowInstanceDoesNotLoseItsFiles(t *testing.T) {
	root := newFixture(t)

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "slow",
		Dir:       root,
		Command:   "[ -f hostname ] && echo found || echo missing",
		Transport: ExecTransport(slowStatTransport{}.Exec),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
	require.NoError(t, err)

	assert.Equal(t, "found\n", out.String(),
		"a file test in a statement waits for the answer; the statement's own ^C ends it")
}

// unreachableTransport is an instance that cannot be reached at all, as
// opposed to one that runs a command and reports a failure.
type unreachableTransport struct{ calls int }

func (t *unreachableTransport) Exec(context.Context, Command) (int, error) {
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
		Transport: ExecTransport(probe.Exec),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not reach sandbox")
	assert.Contains(t, err.Error(), "504 Gateway Time-out")
	assert.Equal(t, instanceProbeAttempts, probe.calls, "asked again, then gave up")
	assert.Empty(t, out.String(), "nothing ran")
}

func TestAnInstanceWithoutAShellStillOpens(t *testing.T) {
	s := &state{cfg: Config{
		Instance:  "quiet",
		Transport: ExecTransport(scriptTransport("").Exec),
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
	s := &state{cfg: Config{
		Transport: ExecTransport(scriptTransport("HOME=/instance\x00PATH=/bin\x00UID=7\x00EUID=7\x00GID=7\x00" +
			"MULTI=a\nLD_PRELOAD=/x.so\nb\x00").Exec),
		Dir: "/srv",
		Env: map[string]string{"EXTRA": "1"},
	}}

	env, err := s.environ(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{
		"EUID=7", "EXTRA=1", "GID=7", "HOME=/instance", "MULTI=a\nLD_PRELOAD=/x.so\nb", "PATH=/bin", "UID=7",
	}, env, "a value spanning lines is one value, not a variable per line")
}

func TestShellNeedsATerminal(t *testing.T) {
	root := newFixture(t)

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Transport: local(),
	}, stdio.Stdio{
		Stdin:  strings.NewReader("cd var/log\nls\n"),
		Stdout: &out,
		Stderr: &out,
	})
	require.ErrorIs(t, err, errNotATerminal)

	assert.Empty(t, out.String())
}

func TestAnUnknownBuiltinIsNamed(t *testing.T) {
	var out captured
	code, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Command:   ":frobnicate",
		Transport: local(),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
	require.NoError(t, err)

	assert.Equal(t, statusBuiltinNotFound, code)
	assert.Equal(t, "unknown builtin \"frobnicate\"; try :help\n", ansi.Strip(out.String()),
		":help is the session's, so there is always one to point at")
}

func TestSessionBuiltinNamesAreReserved(t *testing.T) {
	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Transport: local(),
		Builtins:  builtinsNamed("start", "history"),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
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
		{"history-is-bare-too", "history", 1, "history: only at the prompt\n"},
		{"help-lists-the-session-s-own", ":help", 0, "  :history"},
		{"help-is-bare-too", "help", 0, "  :history"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out captured
			code, err := Run(t.Context(), Config{
				Instance:  "fake",
				Dir:       root,
				Command:   tt.line,
				Transport: local(),
			}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
			require.NoError(t, err)

			assert.Equal(t, tt.code, code)
			assert.Contains(t, ansi.Strip(out.String()), tt.want)
		})
	}
}

func TestThePlatformsHelpComesFirst(t *testing.T) {
	help := map[string]Builtin{
		"help": BuiltinFunc(func(_ context.Context, streams stdio.Stdio, _ []string) (int, error) {
			fmt.Fprintln(streams.Stdout, "  :start     Start the instance.")
			return 0, nil
		}),
	}

	var out captured
	code, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Command:   ":help",
		Transport: local(),
		Builtins:  help,
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
	require.NoError(t, err)

	assert.Zero(t, code)
	assert.Regexp(t, `(?s)^  :start .*\n  :history .*\nEverything else runs on the instance\.\n$`, out.String(),
		"the platform lists its builtins, then the session its own")
}

func TestFailedCommandIsNotAShellFailure(t *testing.T) {
	root := newFixture(t)

	var out captured
	code, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Command:   "false",
		Transport: local(),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})

	require.NoError(t, err, "a failing command is not the shell failing")
	assert.Equal(t, 1, code, "but its status is the session's to report")
}

// exitTransport reports a fixed outcome for every command.
type exitTransport struct {
	code int
	err  error
}

func (t exitTransport) Exec(context.Context, Command) (int, error) {
	return t.code, t.err
}

func TestScriptErrors(t *testing.T) {
	t.Run("non-zero-exit-is-a-missing-path", func(t *testing.T) {
		s := ExecTransport(exitTransport{code: 1}.Exec)

		_, err := s.Stat(t.Context(), "/", "/nope", true)
		assert.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("an-unreachable-instance-says-so", func(t *testing.T) {
		boom := errors.New("504 Gateway Time-out")
		s := ExecTransport(exitTransport{err: boom}.Exec)

		_, err := s.ReadDir(t.Context(), "/", "/")
		require.ErrorIs(t, err, boom)
		assert.NotErrorIs(t, err, fs.ErrNotExist)
	})
}

func TestAccessSeparatesMissingFromDenied(t *testing.T) {
	root := newFixture(t)
	s := local()

	require.NoError(t, s.Access(t.Context(), root, "hostname", AccessRead))

	err := s.Access(t.Context(), root, "nope", AccessRead)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.NotErrorIs(t, err, fs.ErrPermission)

	if os.Geteuid() == 0 {
		t.Skip("root may read anything")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "sealed"), []byte("x"), 0o000))

	err = s.Access(t.Context(), root, "sealed", AccessRead)
	require.ErrorIs(t, err, fs.ErrPermission)
	assert.NotErrorIs(t, err, fs.ErrNotExist)
}

func TestReadDirAnswersForSymlinkedDirectories(t *testing.T) {
	root := newFixture(t)
	require.NoError(t, os.Symlink(filepath.Join(root, "var", "log"), filepath.Join(root, "logs")))

	s := local()

	entries, err := s.ReadDir(t.Context(), root, ".")
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
	s := local()

	_, err := s.ReadDir(t.Context(), root, "hostname")
	require.ErrorIs(t, err, syscall.ENOTDIR)
	require.NotErrorIs(t, err, fs.ErrNotExist)

	_, err = s.ReadDir(t.Context(), root, "nope")
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
	s := ExecTransport(exitTransport{code: -int(syscall.SIGKILL)}.Exec)

	err := s.redirect(t.Context(), "write", "/tmp/out", writeScript, stdio.Stdio{})
	require.ErrorContains(t, err, "signalled (9)",
		"a helper killed under a live statement lost data; that cannot look like success")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	assert.NoError(t, s.redirect(ctx, "write", "/tmp/out", writeScript, stdio.Stdio{}),
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
			Transport: local(),
		}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &buf, Stderr: &buf})
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

	_, err := local().openRead(
		t.Context(), filepath.Join(root, "nope"), io.Discard)

	require.Error(t, err, "the open reports it, not the reader failing later")
}

func TestFileTestsAskTheInstance(t *testing.T) {
	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       "/",
		Command:   `[ -r /etc/hosts ] || echo no-r; [ -w /tmp ] || echo no-w; [ -x /bin/sh ] || echo no-x`,
		Transport: ExecTransport(exitTransport{code: 1}.Exec),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
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

	s := &state{cfg: Config{SuspendSignals: suspend}, interrupts: make(chan os.Signal, 4)}
	release := s.captureInterrupts()

	// Registered after captureInterrupts so that a regression cannot leave
	// SIGINT unhandled and kill this binary
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	assert.Equal(t, []os.Signal{syscall.SIGINT}, suspended)

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	select {
	case <-s.interrupts:
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

	s := &state{interrupts: make(chan os.Signal, 4)}
	assert.NotPanics(t, s.captureInterrupts())
}

func TestStdinReachesTheCommand(t *testing.T) {
	root := newFixture(t)

	var out captured
	_, err := Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       root,
		Command:   "cat",
		Transport: local(),
	}, stdio.Stdio{Stdin: strings.NewReader("payload\n"), Stdout: &out, Stderr: &out})
	require.NoError(t, err)

	assert.Equal(t, "payload\n", out.String())
}

func TestTheInputIsLentToTheCommand(t *testing.T) {
	keys, typed := pipe(t)
	_, err := typed.WriteString("payload\n")
	require.NoError(t, err)

	var out captured
	_, err = Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Command:   "head -1",
		Transport: local(),
		Input:     keys,
	}, stdio.Stdio{Stdout: &out, Stderr: &out})
	require.NoError(t, err)

	assert.Equal(t, "payload\n", out.String())
}

func TestALentFileIsHandedBackAsItWasFound(t *testing.T) {
	keys, typed := pipe(t)

	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	in, reclaim := lendFile(ctx, keys)
	_, err := typed.WriteString("lent\n")
	require.NoError(t, err)

	buf := make([]byte, len("lent\n"))
	n, err := in.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "lent\n", string(buf[:n]))

	reclaim()

	// A file that has handed its descriptor out takes no deadline afterwards,
	// and a read blocked on it could never be cancelled again.
	require.NoError(t, keys.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, err = keys.Read(buf)
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded, "the file no longer takes a deadline")
}

// pipe is a file to read keystrokes off, and the end they are typed into.
func pipe(t *testing.T) (read, write *os.File) {
	t.Helper()

	read, write, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = write.Close()
		_ = read.Close()
	})
	return read, write
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
		Transport: local(),
	}, stdio.Stdio{Stdin: tty, Stdout: &out, Stderr: &out})
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
			Transport: local(),
		}, stdio.Stdio{Stdin: tty, Stdout: &out, Stderr: &out})
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
				Transport: local(),
			}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
			require.ErrorContains(t, err, tt.want)
			assert.Empty(t, out.String(), "refused before anything ran")
		})
	}

	assert.Equal(t, "home\n", runLine(t, root, `HOME=home; echo ~`), "a bare ~ is the instance's HOME, and fine")
}

func TestNewerThanAsksTheInstance(t *testing.T) {
	root := newFixture(t)
	older := filepath.Join(root, "var", "log", "boot.log")
	then := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(older, then, then))

	assert.Equal(t, "newer\n", runLine(t, root, `[ var/log/app.log -nt var/log/boot.log ] && echo newer || echo older`))
	assert.Equal(t, "older\n", runLine(t, root, `[ var/log/boot.log -nt var/log/app.log ] && echo newer || echo older`))
}

// noShellTransport is an instance with no sh: commands run by argv, the helpers do not.
type noShellTransport struct{}

func (t noShellTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	if cmd.Args[0] == "sh" {
		return statusBuiltinNotFound, nil
	}
	return local().Exec(ctx, cmd)
}

func TestAnInstanceWithoutAShellSaysSoOnFiles(t *testing.T) {
	root := newFixture(t)

	var out captured
	code, err := Run(t.Context(), Config{
		Instance:  "bare",
		Dir:       root,
		Command:   "echo runs; cat < var/log/app.log",
		Transport: ExecTransport(noShellTransport{}.Exec),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
	require.NoError(t, err, "the session opens, commands run by argv")

	assert.Equal(t, 1, code)
	assert.Contains(t, out.String(), "runs\n")
	assert.Contains(t, out.String(), ErrNoShell.Error(), "but a redirection says what it is missing, not that the file is")
}

// complainingTransport fails every helper with a word about why.
type complainingTransport struct{}

func (complainingTransport) Exec(_ context.Context, cmd Command) (int, error) {
	fmt.Fprintln(cmd.Streams.Stderr, "sh: out of memory")
	return 2, nil
}

func TestAFailingProbeSaysWhy(t *testing.T) {
	s := ExecTransport(complainingTransport{}.Exec)

	_, err := s.Stat(t.Context(), "/", "/etc", true)
	require.ErrorContains(t, err, "out of memory")
	assert.NotErrorIs(t, err, fs.ErrNotExist, "a probe that complained did not find the path missing")
}

// envTransport answers the environment probe and keeps what the last command
// was given to run with.
type envTransport struct {
	environ string

	mu  sync.Mutex
	env []string
}

func (t *envTransport) Exec(_ context.Context, cmd Command) (int, error) {
	if len(cmd.Args) > 2 && cmd.Args[2] == environProbe {
		fmt.Fprint(cmd.Streams.Stdout, t.environ)
		return 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.env = slices.Clone(cmd.Env)
	return 0, nil
}

func (t *envTransport) lastEnv() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.env)
}

func TestEvalIsVettedLikeALine(t *testing.T) {
	transport := newHaltingTransport()
	s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "eval 'allowed'")
	require.NoError(t, err)
	assert.Contains(t, transport.commands(), "allowed", "a line the vet allows still runs")

	_, err = s.RunLine(t.Context(), "eval 'smuggled &'")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "eval: ")
	assert.Contains(t, out.String(), "& is not supported")
	assert.NotContains(t, transport.commands(), "smuggled",
		"what the vet refuses on a line it refuses through eval too")

	for _, line := range []string{"command eval 'smuggled &'", "command -- eval 'smuggled &'", "builtin eval 'smuggled &'"} {
		_, err = s.RunLine(t.Context(), line)
		require.NoError(t, err)
		assert.NotContains(t, transport.commands(), "smuggled",
			"%s reaches the interpreter's own eval, so the vet looks past the words in front", line)
	}

	asking, _, said := newDrivenSession(t, Config{Transport: ExecTransport(newHaltingTransport().Exec)})
	_, err = asking.RunLine(t.Context(), "command -v eval")
	require.NoError(t, err)
	assert.NotContains(t, said.String(), "eval: ", "asking where eval is runs nothing to vet")
}

func TestEvalCannotSmuggleProcessSubstitution(t *testing.T) {
	transport := newHaltingTransport()
	s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "eval 'true <(:)'")

	require.NoError(t, err)
	assert.Contains(t, out.String(), "process substitution is not supported",
		"the pipe would be made on this machine, in a directory the instance named")
}

func TestEvalSurvivesWhatItCannotParse(t *testing.T) {
	s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(newHaltingTransport().Exec)})

	_, err := s.RunLine(t.Context(), "eval 'for'")

	require.NoError(t, err)
	assert.Contains(t, out.String(), "eval: ")
	assert.False(t, s.Exited(), "a refusal is not the end of the session")
}

func TestSourceIsRefused(t *testing.T) {
	for _, line := range []string{"source /etc/profile", ". /etc/profile", "builtin source /etc/profile"} {
		t.Run(line, func(t *testing.T) {
			transport := newHaltingTransport()
			s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

			_, err := s.RunLine(t.Context(), line)

			require.NoError(t, err)
			assert.Contains(t, out.String(), "is not supported, the file would be read here")
			for _, ran := range transport.commands() {
				assert.NotContains(t, ran, readScript, "the file was never even fetched")
			}
		})
	}
}

func TestATrapActionIsVetted(t *testing.T) {
	t.Run("refuses-what-the-vet-refuses", func(t *testing.T) {
		transport := newHaltingTransport()
		s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

		_, err := s.RunLine(t.Context(), "trap 'smuggled &' EXIT")
		require.NoError(t, err)
		require.NoError(t, s.Close(t.Context()))

		assert.Contains(t, out.String(), "trap: ")
		assert.NotContains(t, transport.commands(), "smuggled",
			"the action never became the session's to run on the way out")
	})

	t.Run("keeps-an-action-it-allows", func(t *testing.T) {
		transport := newHaltingTransport()
		s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

		_, err := s.RunLine(t.Context(), "trap 'on-the-way-out' EXIT")
		require.NoError(t, err)
		require.NoError(t, s.Close(t.Context()))

		assert.Contains(t, transport.commands(), "on-the-way-out", "the trap still fires on the way out")
	})

	t.Run("refuses-an-action-behind-the-end-of-options", func(t *testing.T) {
		transport := newHaltingTransport()
		s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

		_, err := s.RunLine(t.Context(), "trap -- 'smuggled &' EXIT")
		require.NoError(t, err)
		require.NoError(t, s.Close(t.Context()))

		assert.Contains(t, out.String(), "trap: ")
		assert.NotContains(t, transport.commands(), "smuggled",
			"the interpreter reads past --, and so does the vet")
	})

	t.Run("leaves-a-trap-that-only-names-a-signal", func(t *testing.T) {
		s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(newHaltingTransport().Exec)})

		for _, line := range []string{"trap", "trap EXIT", "trap -- EXIT"} {
			_, err := s.RunLine(t.Context(), line)
			require.NoError(t, err)
		}

		assert.NotContains(t, out.String(), "trap: ", "no action is installed, so there is none to vet")
	})

	t.Run("leaves-a-trap-that-clears-one", func(t *testing.T) {
		s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(newHaltingTransport().Exec)})

		_, err := s.RunLine(t.Context(), "trap - EXIT")

		require.NoError(t, err)
		assert.NotContains(t, out.String(), "trap: ", "clearing a trap parses to a command like any other")
	})
}

func TestTheInterpreterKeepsItsOwnTempDir(t *testing.T) {
	kept, tmpdir, exported := withoutTmpdir([]string{"HOME=/", "TMPDIR=/instance/tmp", "PATH=/bin"})

	assert.Equal(t, []string{"HOME=/", "PATH=/bin"}, kept,
		"what the interpreter runs on names no directory of the instance's choosing")
	assert.Equal(t, "/instance/tmp", tmpdir)
	assert.True(t, exported)

	_, tmpdir, exported = withoutTmpdir([]string{"TMPDIR="})
	assert.Empty(t, tmpdir)
	assert.True(t, exported, "an empty one is still one the instance exported")

	_, _, exported = withoutTmpdir([]string{"HOME=/"})
	assert.False(t, exported)
}

func TestACommandStillHasTheInstancesTempDir(t *testing.T) {
	transport := &envTransport{environ: "HOME=/\x00PATH=/bin\x00TMPDIR=/instance/tmp\x00"}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "somewhere")

	require.NoError(t, err)
	assert.Contains(t, transport.lastEnv(), "TMPDIR=/instance/tmp",
		"the instance's own commands keep the directory the instance exported")
}

func TestACommandKeepsAnEmptyTempDir(t *testing.T) {
	transport := &envTransport{environ: "HOME=/\x00PATH=/bin\x00TMPDIR=\x00"}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "somewhere")

	require.NoError(t, err)
	assert.Contains(t, transport.lastEnv(), "TMPDIR=",
		"the instance exported it empty, so a command still sees it empty and not unset")
}

// floodTransport answers one command with output that never ends, and keeps
// the context it was handed so a test can see it cut short.
type floodTransport struct {
	mu      sync.Mutex
	stopped bool
}

func (t *floodTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	if cmd.Args[0] != "flood" {
		return 0, nil
	}
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for {
		_, err := cmd.Streams.Stdout.Write(chunk)
		if err == nil && ctx.Err() == nil {
			continue
		}
		t.mu.Lock()
		t.stopped = ctx.Err() != nil
		t.mu.Unlock()
		return StatusInterrupted, nil
	}
}

func (t *floodTransport) cutShort() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

func TestACaptureIsCapped(t *testing.T) {
	transport := &floodTransport{}
	s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	status, err := s.RunLine(t.Context(), "captured=$(flood)")

	require.NoError(t, err)
	assert.Equal(t, 1, status, "the command substitution failed rather than filling this machine")
	assert.Contains(t, out.String(), "command substitution kept more than 8 MiB")
	assert.True(t, transport.cutShort(), "the instance was told to stop, not left to write on")
}

func TestASmallCaptureIsUntouched(t *testing.T) {
	transport := scriptTransport("hi\n")
	s, _, out := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	status, err := s.RunLine(t.Context(), "captured=$(say-hi); printf %s \"$captured\"")

	require.NoError(t, err)
	assert.Zero(t, status)
	assert.Contains(t, out.String(), "hi", "what fits is kept byte for byte")
}

func TestAStreamIsNotCapped(t *testing.T) {
	var written int64
	transport := ExecTransport(func(ctx context.Context, cmd Command) (int, error) {
		if cmd.Args[0] != "flood" {
			return 0, nil
		}
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for range 200 {
			n, err := cmd.Streams.Stdout.Write(chunk)
			written += int64(n)
			if err != nil {
				return 1, nil
			}
		}
		return 0, nil
	})
	s, _, _ := newDrivenSession(t, Config{Transport: transport})

	status, err := s.RunLine(t.Context(), "flood > /dev/null")

	require.NoError(t, err)
	assert.Zero(t, status)
	assert.Equal(t, int64(200*(64<<10)), written,
		"a stream answers for its own size, so the cap left it alone")
}

func TestAProbeIsCapped(t *testing.T) {
	flood := ExecTransport(func(_ context.Context, cmd Command) (int, error) {
		chunk := bytes.Repeat([]byte("d name\x00"), 1<<10)
		for range 1 << 10 {
			if _, err := cmd.Streams.Stdout.Write(chunk); err != nil {
				return 1, nil
			}
		}
		return 0, nil
	})

	_, err := flood.ReadDir(t.Context(), "/", "big")

	require.ErrorContains(t, err, "more than 4 MiB",
		"a directory listing is not the instance's to make this machine hold")
}

func TestABuiltinsCaptureIsCapped(t *testing.T) {
	flooding := map[string]Builtin{
		"flood": BuiltinFunc(func(ctx context.Context, streams stdio.Stdio, _ []string) (int, error) {
			chunk := bytes.Repeat([]byte("x"), 64<<10)
			for ctx.Err() == nil {
				if _, err := streams.Stdout.Write(chunk); err != nil {
					return 1, nil
				}
			}
			return 0, nil
		}),
	}
	s, _, out := newDrivenSession(t, Config{
		Transport: ExecTransport(scriptTransport("").Exec),
		Builtins:  flooding,
	})

	status, err := s.RunLine(t.Context(), "captured=$(:flood)")

	require.NoError(t, err)
	assert.Equal(t, 1, status, "a builtin fills a command substitution as readily as the instance")
	assert.Contains(t, out.String(), "command substitution kept more than 8 MiB")
}

func TestAProbesComplaintIsCapped(t *testing.T) {
	flood := ExecTransport(func(_ context.Context, cmd Command) (int, error) {
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for range 1 << 7 {
			if _, err := cmd.Streams.Stderr.Write(chunk); err != nil {
				return 1, nil
			}
		}
		return 1, nil
	})

	_, err := flood.Stat(t.Context(), "/", "somewhere", true)

	require.ErrorContains(t, err, "more than 4 MiB",
		"what the helper complains with is held here too")
}

func TestAFloodingProbeIsCutShort(t *testing.T) {
	var wrote int64
	flood := ExecTransport(func(ctx context.Context, cmd Command) (int, error) {
		chunk := bytes.Repeat([]byte("d name\x00"), 1<<10)
		for ctx.Err() == nil {
			n, err := cmd.Streams.Stdout.Write(chunk)
			wrote += int64(n)
			if err != nil {
				return 1, nil
			}
		}
		return 1, nil
	})

	_, err := flood.ReadDir(t.Context(), "/", "big")

	require.ErrorContains(t, err, "more than 4 MiB")
	assert.Less(t, wrote, int64(2*maxProbeOutput),
		"the instance was told to stop rather than left to send the rest")
}

func TestTheEnvironmentProbeIsCapped(t *testing.T) {
	flood := ExecTransport(func(ctx context.Context, cmd Command) (int, error) {
		chunk := bytes.Repeat([]byte("NAME=value\x00"), 1<<10)
		for ctx.Err() == nil {
			if _, err := cmd.Streams.Stdout.Write(chunk); err != nil {
				return 1, nil
			}
		}
		return 1, nil
	})

	_, err := flood.Environ(t.Context())

	require.ErrorContains(t, err, "more than 4 MiB",
		"an instance does not get to fill this machine while the session is opening")
}

// nodeError is what a transport reaching a node over HTTP fails with.
func nodeError() error {
	return fmt.Errorf("failed to start command: %w", &url.Error{
		Op:  "Post",
		URL: "https://node-7.fra0.example/v1/instances/abc/plugins/sandbox/commands?token=s3cret",
		Err: &net.OpError{
			Op:   "dial",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 443},
			Err:  errors.New("connect: connection refused"),
		},
	})
}

// failingTransport opens a session and then fails every command it is asked to run.
func failingTransport(err error) ExecTransport {
	return func(_ context.Context, cmd Command) (int, error) {
		if len(cmd.Args) > 2 && cmd.Args[2] == environProbe {
			return 0, nil
		}
		return 0, err
	}
}

func TestATransportErrorKeepsTheNodeToItself(t *testing.T) {
	s, _, out := newDrivenSession(t, Config{Transport: failingTransport(nodeError())})

	status, err := s.RunLine(t.Context(), "somewhere")

	require.NoError(t, err)
	assert.Equal(t, 1, status)
	said := out.String()
	assert.NotContains(t, said, "node-7.fra0.example", "the node the transport reached is not the user's business")
	assert.NotContains(t, said, "s3cret")
	assert.NotContains(t, said, "10.0.0.5")
	assert.Contains(t, said, "failed to start command", "what the transport wrote for a person survives")
	assert.Contains(t, said, "connection refused", "and so does why it failed")
}

func TestAnErrorKeepsWhatTheTransportSaid(t *testing.T) {
	plain := errors.New("the instance has no sh")

	assert.Same(t, plain, sanitised(plain), "an error naming no address is the one it was given")
	assert.NoError(t, sanitised(nil))
}

func TestASanitisedErrorIsStillTheOneItWasMadeFrom(t *testing.T) {
	err := sanitised(fmt.Errorf("reading: %w", &url.Error{
		Op: "Get", URL: "https://node-7.example/logs", Err: fs.ErrNotExist,
	}))

	require.ErrorIs(t, err, fs.ErrNotExist, "what the shell tests errors for still answers")
	assert.NotContains(t, err.Error(), "node-7.example")
	assert.Contains(t, err.Error(), "reading: Get: ")
}

func TestAProbeErrorKeepsTheNodeToItself(t *testing.T) {
	_, err := failingTransport(nodeError()).Stat(t.Context(), "/", "somewhere", true)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "node-7.fra0.example",
		"a file question fails the same way a command does")
}

// askedTransport answers the instance's file questions and keeps what it was
// asked, so a test can count the round trips a line costs.
type askedTransport struct {
	environ string
	kind    string

	mu    sync.Mutex
	asked []string
}

func (t *askedTransport) Exec(_ context.Context, cmd Command) (int, error) {
	snippet := ""
	if len(cmd.Args) > 2 {
		snippet = cmd.Args[2]
	}

	switch snippet {
	case environProbe:
		fmt.Fprint(cmd.Streams.Stdout, cmp.Or(t.environ, "EUID=0\x00"))
		return 0, nil
	case statScript:
		t.record("stat " + cmd.Args[4])
		fmt.Fprintln(cmd.Streams.Stdout, cmp.Or(t.kind, "d")+" 0 0 -")
	case accessScript:
		t.record("access " + cmd.Args[4])
		fmt.Fprintln(cmd.Streams.Stdout, "ok")
	default:
		t.record("run " + cmd.Args[0])
	}
	return 0, nil
}

func (t *askedTransport) record(what string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.asked = append(t.asked, what)
}

func (t *askedTransport) questions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.asked)
}

func TestCdAsksTheInstanceOnce(t *testing.T) {
	transport := &askedTransport{}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "cd /var/log")

	require.NoError(t, err)
	assert.Equal(t, "/var/log", s.Dir())
	assert.Equal(t, []string{"stat /var/log"}, transport.questions(),
		"a stat already says root may enter the directory it describes")
}

func TestTheSamePathIsAskedAfterOnce(t *testing.T) {
	transport := &askedTransport{}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "cd /var/log")
	require.NoError(t, err)
	_, err = s.RunLine(t.Context(), "cd /var/log")
	require.NoError(t, err)

	assert.Equal(t, []string{"stat /var/log"}, transport.questions(),
		"nothing ran on the instance in between, so the answer still holds")
}

func TestWhatRanOnTheInstanceIsNotRemembered(t *testing.T) {
	transport := &askedTransport{}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "cd /var/log; something; cd /var/log")

	require.NoError(t, err)
	assert.Equal(t, []string{"stat /var/log", "run something", "stat /var/log"},
		transport.questions(), "a command may have moved what was there")
}

func TestANonRootSessionStillAsksAboutAccess(t *testing.T) {
	transport := &askedTransport{environ: "EUID=1000\x00"}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "cd /var/log")

	require.NoError(t, err)
	assert.Equal(t, []string{"stat /var/log", "access /var/log"}, transport.questions(),
		"only root enters a directory whatever its mode says")
}

func TestOnlyADirectoryIsAnsweredFromAStat(t *testing.T) {
	transport := &askedTransport{kind: "f"}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "[ -x /bin/tool ]")

	require.NoError(t, err)
	assert.Contains(t, transport.questions(), "access /bin/tool",
		"whether a file runs is the instance's to say, the stat not carrying its mode")
}

func TestTheSessionCannotTalkItselfIntoBeingRoot(t *testing.T) {
	transport := &askedTransport{environ: "EUID=1000\x00"}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "export EUID=0; cd /var/log")

	require.NoError(t, err)
	assert.Contains(t, transport.questions(), "access /var/log",
		"who the session is was settled by the instance, not by what it later assigned")
}

func TestTheCommandLineCannotEitherClaimRoot(t *testing.T) {
	transport := &askedTransport{environ: "EUID=1000\x00"}
	s, _, _ := newDrivenSession(t, Config{
		Transport: ExecTransport(transport.Exec),
		Env:       map[string]string{"EUID": "0"},
	})

	_, err := s.RunLine(t.Context(), "cd /var/log")

	require.NoError(t, err)
	assert.Contains(t, transport.questions(), "access /var/log",
		"an environment the caller passed in is not the instance speaking")
}

func TestAStatThatFailedIsAskedAgain(t *testing.T) {
	transport := &refusingStatTransport{}
	s, _, _ := newDrivenSession(t, Config{Transport: ExecTransport(transport.Exec)})

	_, err := s.RunLine(t.Context(), "[ -d /gone ]; [ -d /gone ]")

	require.NoError(t, err)
	assert.Equal(t, 2, transport.stats,
		"a question the instance could not answer is not an answer to keep")
}

// refusingStatTransport opens a session and then fails every stat it is asked.
type refusingStatTransport struct{ stats int }

func (t *refusingStatTransport) Exec(_ context.Context, cmd Command) (int, error) {
	if len(cmd.Args) > 2 && cmd.Args[2] == statScript {
		t.stats++
		return 0, errors.New("the instance is not answering")
	}
	return 0, nil
}

func TestAStatOvertakenByACommandIsNotKept(t *testing.T) {
	s := &state{}
	key := statKey{path: "/var/log", follow: true}

	// What a pipeline does: one branch asks about a path while the other runs
	// something on the instance and finishes first.
	asOf := s.statsAsOf()
	s.forgetStats()
	s.rememberStat(key, remoteFileInfo{name: "log", kind: "d"}, asOf)

	_, asked := s.recalledStat(key)
	assert.False(t, asked, "the answer described the instance as it was before that command")

	asOf = s.statsAsOf()
	s.rememberStat(key, remoteFileInfo{name: "log", kind: "d"}, asOf)
	_, asked = s.recalledStat(key)
	assert.True(t, asked, "nothing ran while this one was being asked")
}
