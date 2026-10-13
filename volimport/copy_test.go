// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package volimport

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unikraft/go-cpio"
)

const testAuth = "0123456789abcdefghijklmnopqrstuv"

type entry struct {
	name string
	data string
}

// testArchive returns a CPIO archive with a directory, a file larger than one
// message and a symlink.
func testArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := cpio.NewWriter(&buf)
	add := func(hdr *cpio.Header, data string) {
		hdr.Size = int64(len(data))
		require.NoError(t, w.WriteHeader(hdr))
		_, err := io.WriteString(w, data)
		require.NoError(t, err)
	}
	add(&cpio.Header{Name: "dir", Mode: cpio.TypeDir | 0o755}, "")
	add(&cpio.Header{Name: "dir/file", Mode: 0o644}, strings.Repeat("a", msgMaxSize+1))
	add(&cpio.Header{Name: "link", Mode: cpio.TypeSymlink | 0o777}, "dir/file")
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// writeFrame sends one service frame with status and payload.
func writeFrame(conn net.Conn, status int32, payload []byte) error {
	head := binary.LittleEndian.AppendUint32(nil, uint32(status))
	head = binary.LittleEndian.AppendUint32(head, uint32(len(payload)))
	_, err := conn.Write(append(head, payload...))
	return err
}

// stats encodes the volume space as the service sends it.
func stats(free, total uint64) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, stopResponse{Free: free, Total: total, Maxlen: 255})
	return buf.Bytes()
}

// serve acts as the import service on conn. It sends the entries that it gets
// on the channel, or closes the channel on an error.
func serve(conn net.Conn, free uint64, failAfter int) <-chan []entry {
	got := make(chan []entry, 1)
	go func() {
		defer close(got)
		auth := make([]byte, len(testAuth))
		if _, err := io.ReadFull(conn, auth); err != nil || string(auth) != testAuth {
			return
		}
		if writeFrame(conn, 2, stats(free, 1<<30)) != nil {
			return
		}
		var entries []entry
		for {
			hdr := make([]byte, 110)
			if _, err := io.ReadFull(conn, hdr); err != nil {
				return
			}
			size, _ := strconv.ParseUint(string(hdr[54:62]), 16, 64)
			nameSize, _ := strconv.ParseUint(string(hdr[94:102]), 16, 64)
			name := make([]byte, nameSize)
			if _, err := io.ReadFull(conn, name); err != nil {
				return
			}
			if string(name) == "TRAILER!!!\x00" {
				if writeFrame(conn, 2, stats(7, 1<<30)) != nil {
					return
				}
				if _, err := io.ReadFull(conn, auth); err != nil {
					return
				}
				got <- entries
				return
			}
			data := make([]byte, size)
			if _, err := io.ReadFull(conn, data); err != nil {
				return
			}
			entries = append(entries, entry{name: string(name[:nameSize-1]), data: string(data)})
			if len(entries) == failAfter {
				_ = writeFrame(conn, -1, []byte("disk full\n\x00"))
				_ = writeFrame(conn, 0, nil)
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			if writeFrame(conn, 1, nil) != nil {
				return
			}
		}
	}()
	return got
}

func pipe(t *testing.T) (client, server net.Conn) {
	client, server = net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

func TestCopySendsEveryEntry(t *testing.T) {
	archive := testArchive(t)
	client, server := pipe(t)
	got := serve(server, 1<<20, 0)

	free, total, err := Copy(t.Context(), client, testAuth, bytes.NewReader(archive), false, uint64(len(archive)))
	require.NoError(t, err)
	require.Equal(t, uint64(7), free)
	require.Equal(t, uint64(1<<30), total)
	require.Equal(t, []entry{
		{name: "dir"},
		{name: "dir/file", data: strings.Repeat("a", msgMaxSize+1)},
		{name: "link", data: "dir/file"},
	}, <-got)
}

func TestCopyNoSpace(t *testing.T) {
	archive := testArchive(t)
	client, server := pipe(t)
	serve(server, 1, 0)

	_, _, err := Copy(t.Context(), client, testAuth, bytes.NewReader(archive), false, uint64(len(archive)))
	require.ErrorContains(t, err, "not enough free space on volume")
}

func TestCopyNoSpaceForce(t *testing.T) {
	archive := testArchive(t)
	client, server := pipe(t)
	got := serve(server, 1, 0)

	free, _, err := Copy(t.Context(), client, testAuth, bytes.NewReader(archive), true, uint64(len(archive)))
	require.NoError(t, err)
	require.Equal(t, uint64(7), free)
	require.Len(t, <-got, 3)
}

func TestCopyServiceError(t *testing.T) {
	archive := testArchive(t)
	client, server := pipe(t)
	serve(server, 1<<20, 1)

	_, _, err := Copy(t.Context(), client, testAuth, bytes.NewReader(archive), false, uint64(len(archive)))
	require.EqualError(t, err, "transmission failed: disk full")
}
