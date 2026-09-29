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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	xio "unikraft.com/x/io"
	"unikraft.com/x/stdio"
)

// What goes wrong between the shell and the instance: a transport that fails
// mid-command, helpers that die or babble, files that cannot be read.

func TestTheInstanceRunsTheBuiltinsTheInterpreterLacks(t *testing.T) {
	root := newFixture(t)

	assert.Regexp(t, `^[0-7]{3,4}\n$`, runLine(t, root, `umask`), "umask is the instance's, not \"unsupported builtin\"")
	assert.Regexp(t, `^([0-9]+|unlimited)\n$`, runLine(t, root, `ulimit -n`))
	assert.Equal(t, "KILL\n", runLine(t, root, `kill -l 9`))
}

func TestAnUnreadableFileIsStillThere(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may read anything")
	}
	root := newFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "sealed"), []byte("secret"), 0o000))

	assert.Equal(t, "there\nfile\nsized\n",
		runLine(t, root, `[ -e sealed ] && echo there; [ -f sealed ] && echo file; [ -s sealed ] && echo sized`),
		"stat needs no permission on the file; a size wc cannot count is not a missing file")
}

func TestASizeIsAskedForNotCounted(t *testing.T) {
	root := newFixture(t)
	big := filepath.Join(root, "big.img")
	require.NoError(t, os.WriteFile(big, nil, 0o644))
	require.NoError(t, os.Truncate(big, 5<<30))

	s := local()

	started := time.Now()
	info, err := s.Stat(t.Context(), root, "big.img", true)
	require.NoError(t, err)

	assert.Equal(t, int64(5<<30), info.Size(), "past 32 bits, and not read to be counted")
	assert.Less(t, time.Since(started), 5*time.Second)
}

func TestLstatKeepsTheSpecialBitsToTheLink(t *testing.T) {
	root := newFixture(t)
	target := filepath.Join(root, "suid")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755))
	if err := os.Chmod(target, 0o755|os.ModeSetuid); err != nil {
		t.Skip("cannot set setuid here:", err)
	}
	require.NoError(t, os.Symlink("suid", filepath.Join(root, "link")))

	s := local()

	followed, err := s.Stat(t.Context(), root, "link", true)
	require.NoError(t, err)
	assert.NotZero(t, followed.Mode()&fs.ModeSetuid, "stat follows to the file")

	link, err := s.Stat(t.Context(), root, "link", false)
	require.NoError(t, err)
	assert.Zero(t, link.Mode()&fs.ModeSetuid, "lstat reports the link, which has no bits of its own")
	assert.NotZero(t, link.Mode()&fs.ModeSymlink)
}

func TestAStatRecordIsTheLastLine(t *testing.T) {
	t.Run("a-banner-before-it-is-skipped", func(t *testing.T) {
		s := ExecTransport(scriptTransport("Warning: motd\nf 12 5 -\n").Exec)

		info, err := s.Stat(t.Context(), "/", "/etc/hosts", true)
		require.NoError(t, err)
		assert.Equal(t, int64(12), info.Size())
		assert.Zero(t, info.Mode()&fs.ModeSetuid, "- is no special bits")
	})

	t.Run("a-short-record-is-refused", func(t *testing.T) {
		s := ExecTransport(scriptTransport("f 12 5\n").Exec)

		_, err := s.Stat(t.Context(), "/", "/etc/hosts", true)
		require.ErrorContains(t, err, "unexpected stat output")
	})
}

func TestTheTerminalShowsThroughTheConsole(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })

	var mu sync.Mutex
	assert.True(t, xio.IsTTY(lockWriter(&mu, tty)), "a builtin colours as it would outside the shell")
	_, hasFd := lockWriter(&mu, &bytes.Buffer{}).(interface{ Fd() uintptr })
	assert.False(t, hasFd, "a buffer does not pretend to one")

	seen := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := ptmx.Read(buf)
		seen <- string(buf[:n])
	}()

	_, err = Run(t.Context(), Config{
		Instance:  "fake",
		Dir:       newFixture(t),
		Command:   `[ -t 1 ] && echo tty || echo pipe`,
		Transport: local(),
	}, stdio.Stdio{Stdin: strings.NewReader(""), Stdout: tty, Stderr: tty})
	require.NoError(t, err)

	select {
	case got := <-seen:
		assert.Equal(t, "tty", strings.TrimSpace(got), "[ -t 1 ] sees the terminal under the console")
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the terminal")
	}
}

// helperTransport runs everything here, except the helper whose script contains
// marker: that one is answered by answer.
type helperTransport struct {
	marker string
	answer func(ctx context.Context, streams stdio.Stdio) (int, error)
}

func (t helperTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	if len(cmd.Args) > 2 && cmd.Args[0] == "sh" && strings.Contains(cmd.Args[2], t.marker) {
		return t.answer(ctx, cmd.Streams)
	}
	return local().Exec(ctx, cmd)
}

// crlf turns every newline written through it into CRLF, as a pty would.
type crlf struct{ w io.Writer }

func (c crlf) Write(p []byte) (int, error) {
	_, err := c.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n")))
	return len(p), err
}

func TestAWriteAckMayEndInCRLF(t *testing.T) {
	root := newFixture(t)
	out := filepath.Join(root, "out.txt")

	transport := helperTransport{marker: "3>", answer: func(ctx context.Context, streams stdio.Stdio) (int, error) {
		streams.Stdout = crlf{streams.Stdout}
		return local().Exec(ctx, Command{
			Args:    []string{"sh", "-c", writeScript, "sh", out},
			Dir:     root,
			Streams: streams,
		})
	}}

	var buf captured
	_, err := Run(t.Context(), Config{Instance: "fake", Dir: root, Command: "echo written > " + out, Transport: ExecTransport(transport.Exec)},
		stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &buf, Stderr: &buf})
	require.NoError(t, err)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "written\n", string(data))
	assert.Empty(t, buf.String())
}

func TestAWriteHelperThatBabblesFailsPromptly(t *testing.T) {
	helper := helperTransport{marker: "3>", answer: func(_ context.Context, streams stdio.Stdio) (int, error) {
		fmt.Fprintln(streams.Stdout, "Warning: this instance is about to be retired")
		fmt.Fprintln(streams.Stdout, "and more where that came from")
		fmt.Fprintln(streams.Stderr, "sh: banner")
		return 1, nil
	}}
	s := ExecTransport(helper.Exec)

	done := make(chan error, 1)
	go func() {
		_, err := s.openWrite(t.Context(), "/tmp/out", false, io.Discard)
		done <- err
	}()

	select {
	case err := <-done:
		require.ErrorContains(t, err, "banner")
	case <-time.After(5 * time.Second):
		t.Fatal("a helper with more than an ack to say held the open forever")
	}
}

func TestAWriteThatNeverOpensIsInvalid(t *testing.T) {
	t.Run("a-helper-that-says-nothing", func(t *testing.T) {
		s := ExecTransport(exitTransport{}.Exec)

		_, err := s.openWrite(t.Context(), "/tmp/out", false, io.Discard)
		require.ErrorIs(t, err, fs.ErrInvalid)
	})

	t.Run("an-interrupted-statement-writes-nowhere", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		s := ExecTransport(exitTransport{err: context.Canceled}.Exec)

		w, err := s.openWrite(ctx, "/tmp/out", false, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, xio.DevNull, w, "the command was interrupted; there is nothing to report")
	})
}

func TestAWriteThatFailsAfterTheAckIsReported(t *testing.T) {
	root := newFixture(t)

	transport := helperTransport{marker: "3>", answer: func(_ context.Context, streams stdio.Stdio) (int, error) {
		fmt.Fprintln(streams.Stdout, "ok")
		_, _ = io.Copy(io.Discard, streams.Stdin)
		fmt.Fprintln(streams.Stderr, "sh: 1: cannot write: No space left on device")
		return 1, nil
	}}

	var buf captured
	_, err := Run(t.Context(), Config{Instance: "fake", Dir: root, Command: "echo data > /var/log/full; echo status=$?", Transport: ExecTransport(transport.Exec)},
		stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &buf, Stderr: &buf})
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "No space left on device", "the only word of a redirection that died writing")
}

func TestAReadThatFailsMidStreamIsReported(t *testing.T) {
	root := newFixture(t)

	transport := helperTransport{marker: "cat --", answer: func(_ context.Context, streams stdio.Stdio) (int, error) {
		fmt.Fprintln(streams.Stdout, "partial")
		fmt.Fprintln(streams.Stderr, "cat: read error: Input/output error")
		return 1, nil
	}}

	var buf captured
	_, err := Run(t.Context(), Config{Instance: "fake", Dir: root, Command: "cat < /var/log/app.log", Transport: ExecTransport(transport.Exec)},
		stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &buf, Stderr: &buf})
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "partial\n", "what did arrive is not held back")
	assert.Contains(t, buf.String(), "Input/output error", "and a short read does not pass for EOF")
}

func TestAnEarlyReaderIsNotAnError(t *testing.T) {
	root := newFixture(t)
	big := filepath.Join(root, "big.txt")
	require.NoError(t, os.WriteFile(big, bytes.Repeat([]byte("0123456789\n"), 400000), 0o644))

	assert.Equal(t, "0123456789\n", runLine(t, root, "head -1 < "+big),
		"the helper is killed by the pipe closing under it; that is the reader's doing, not a failure")
}

func TestAFailingBuiltinReportsItsError(t *testing.T) {
	root := newFixture(t)

	builtins := map[string]Builtin{
		"fail": BuiltinFunc(func(_ context.Context, _ stdio.Stdio, _ []string) (int, error) {
			return 0, errors.New("could not fail properly")
		}),
		"failcode": BuiltinFunc(func(_ context.Context, _ stdio.Stdio, _ []string) (int, error) {
			return 3, errors.New("failed with a status of its own")
		}),
	}

	for _, tt := range []struct {
		line string
		code int
		want string
	}{
		{":fail", 1, "could not fail properly"},
		{":failcode", 3, "failed with a status of its own"},
	} {
		t.Run(tt.line, func(t *testing.T) {
			var buf captured
			code, err := Run(t.Context(), Config{Instance: "fake", Dir: root, Command: tt.line, Transport: local(), Builtins: builtins},
				stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &buf, Stderr: &buf})
			require.NoError(t, err)

			assert.Equal(t, tt.code, code)
			assert.Equal(t, tt.want+"\n", buf.String())
		})
	}
}

// droppingTransport reaches the instance for the helpers but loses the
// connection under every command.
type droppingTransport struct{}

func (t droppingTransport) Exec(ctx context.Context, cmd Command) (int, error) {
	if cmd.Args[0] != "sh" {
		return 0, errors.New("connection reset by peer")
	}
	return local().Exec(ctx, cmd)
}

func TestACommandTheTransportLosesIsAFailedCommand(t *testing.T) {
	root := newFixture(t)

	var buf captured
	_, err := Run(t.Context(), Config{Instance: "fake", Dir: root, Command: "uptime; echo status=$?", Transport: ExecTransport(droppingTransport{}.Exec)},
		stdio.Stdio{Stdin: strings.NewReader(""), Stdout: &buf, Stderr: &buf})
	require.NoError(t, err, "the session goes on")

	assert.Regexp(t, `^connection reset by peer\nstatus=1\n$`, buf.String())
}
