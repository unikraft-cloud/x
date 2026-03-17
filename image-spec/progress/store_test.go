// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package progress

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// collectingTracker collects all progress updates for assertions.
type collectingTracker struct {
	mu      sync.Mutex
	updates []Progress
}

func (t *collectingTracker) Update(p Progress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.updates = append(t.updates, p)
}

func (t *collectingTracker) Updates() []Progress {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Progress{}, t.updates...)
}

// memoryReaderAt implements content.ReaderAt backed by a byte slice.
type memoryReaderAt struct {
	*bytes.Reader
}

func (m *memoryReaderAt) Close() error {
	return nil
}

// memoryProvider implements content.Provider backed by in-memory data.
type memoryProvider struct {
	data map[digest.Digest][]byte
}

func (m *memoryProvider) ReaderAt(_ context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	data, ok := m.data[desc.Digest]
	if !ok {
		return nil, errNotFound("not found")
	}
	return &memoryReaderAt{Reader: bytes.NewReader(data)}, nil
}

type errNotFound string

func (e errNotFound) Error() string { return string(e) }
func (e errNotFound) NotFound()     {}

// memoryIngester implements content.Ingester with in-memory writers.
type memoryIngester struct {
	commitErr error
}

func (m *memoryIngester) Writer(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
	return &memoryWriter{commitErr: m.commitErr}, nil
}

// memoryWriter implements content.Writer backed by a bytes buffer.
type memoryWriter struct {
	buf       bytes.Buffer
	closed    bool
	committed bool
	commitErr error
}

func (m *memoryWriter) Write(p []byte) (n int, err error) {
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	return m.buf.Write(p)
}

func (m *memoryWriter) Close() error {
	m.closed = true
	return nil
}

func (m *memoryWriter) Digest() digest.Digest {
	if !m.committed {
		return ""
	}
	return digest.FromBytes(m.buf.Bytes())
}

func (m *memoryWriter) Commit(_ context.Context, _ int64, _ digest.Digest, _ ...content.Opt) error {
	m.closed = true
	if m.commitErr != nil {
		return m.commitErr
	}
	m.committed = true
	return nil
}

func (m *memoryWriter) Status() (content.Status, error) {
	return content.Status{Offset: int64(m.buf.Len())}, nil
}

func (m *memoryWriter) Truncate(size int64) error {
	if size < 0 {
		return errors.New("negative truncate size")
	}
	b := m.buf.Bytes()
	if int(size) <= len(b) {
		m.buf.Reset()
		_, _ = m.buf.Write(b[:size])
		return nil
	}

	padding := make([]byte, int(size)-len(b))
	_, _ = m.buf.Write(padding)
	return nil
}

func TestWrapProviderEmitsProgress(t *testing.T) {
	data := []byte("hello world test data for progress tracking")
	dgst := digest.FromBytes(data)

	provider := &memoryProvider{
		data: map[digest.Digest][]byte{dgst: data},
	}

	tracker := &collectingTracker{}
	ctx := WithTracker(context.Background(), tracker)

	desc := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Size:      int64(len(data)),
		Digest:    dgst,
	}

	wrapped := WrapProvider(provider)
	ra, err := wrapped.ReaderAt(ctx, desc)
	require.NoError(t, err)

	// Initial progress update should have been sent
	updates := tracker.Updates()
	require.NotEmpty(t, updates)
	assert.Equal(t, int64(0), updates[0].Current)
	assert.Equal(t, int64(len(data)), updates[0].Total)
	assert.Equal(t, ActionDownloading, updates[0].Action)

	// Read data
	buf := make([]byte, len(data))
	_, err = ra.ReadAt(buf, 0)
	require.True(t, err == nil || err == io.EOF, "unexpected read error: %v", err)

	require.NoError(t, ra.Close())

	updates = tracker.Updates()
	require.NotEmpty(t, updates)
	last := updates[len(updates)-1]
	assert.False(t, last.Completed.IsZero())
	assert.Equal(t, ActionDownloading, last.Action)
}

func TestWrapProviderNoTracker(t *testing.T) {
	data := []byte("test")
	dgst := digest.FromBytes(data)

	provider := &memoryProvider{
		data: map[digest.Digest][]byte{dgst: data},
	}

	// No tracker in context
	ctx := context.Background()

	desc := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Size:      int64(len(data)),
		Digest:    dgst,
	}

	wrapped := WrapProvider(provider)
	ra, err := wrapped.ReaderAt(ctx, desc)
	require.NoError(t, err)

	// Should still work without tracker
	buf := make([]byte, len(data))
	_, err = ra.ReadAt(buf, 0)
	require.True(t, err == nil || err == io.EOF, "unexpected read error: %v", err)
	require.NoError(t, ra.Close())
}

func TestWrapIngesterEmitsProgress(t *testing.T) {
	ingester := &memoryIngester{}

	tracker := &collectingTracker{}
	ctx := WithTracker(context.Background(), tracker)

	data := []byte("hello upload progress")
	desc := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Size:      int64(len(data)),
		Digest:    digest.FromBytes(data),
	}

	wrapped := WrapIngester(ingester)
	w, err := wrapped.Writer(ctx, content.WithDescriptor(desc))
	require.NoError(t, err)

	updates := tracker.Updates()
	require.NotEmpty(t, updates)
	assert.Equal(t, ActionUploading, updates[0].Action)
	assert.Equal(t, int64(0), updates[0].Current)
	assert.Equal(t, desc.Size, updates[0].Total)

	_, err = w.Write(data[:5])
	require.NoError(t, err)
	_, err = w.Write(data[5:])
	require.NoError(t, err)

	err = w.Commit(ctx, desc.Size, desc.Digest)
	require.NoError(t, err)

	updates = tracker.Updates()
	require.GreaterOrEqual(t, len(updates), 4) // initial + writes + final
	last := updates[len(updates)-1]
	assert.Equal(t, ActionUploading, last.Action)
	assert.Equal(t, int64(len(data)), last.Current)
	assert.False(t, last.Completed.IsZero())
}

func TestWrapIngesterFailedCommitStillEmitsTerminalUpdateWithLimiter(t *testing.T) {
	ingester := &memoryIngester{commitErr: errors.New("commit failed")}

	base := &collectingTracker{}
	limited := WithLimiter(base, rate.NewLimiter(0, 0))
	ctx := WithTracker(context.Background(), limited)

	data := []byte("upload with failure")
	desc := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Size:      int64(len(data)),
		Digest:    digest.FromBytes(data),
	}

	wrapped := WrapIngester(ingester)
	w, err := wrapped.Writer(ctx, content.WithDescriptor(desc))
	require.NoError(t, err)

	_, err = w.Write(data)
	require.NoError(t, err)

	err = w.Commit(ctx, desc.Size, desc.Digest)
	require.EqualError(t, err, "commit failed")

	updates := base.Updates()
	require.Len(t, updates, 2) // first + terminal (intermediate update rate-limited)
	assert.Equal(t, int64(0), updates[0].Current)
	assert.Equal(t, int64(len(data)), updates[1].Current)
	assert.False(t, updates[1].Completed.IsZero())
}

func TestFromContextAndG(t *testing.T) {
	ctx := context.Background()

	// No tracker
	_, ok := FromContext(ctx)
	require.False(t, ok)

	// G should return nop tracker
	tracker := G(ctx)
	require.NotNil(t, tracker)
	// Should not panic
	tracker.Update(Progress{})

	// With tracker
	ct := &collectingTracker{}
	ctx = WithTracker(ctx, ct)
	got, ok := FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, ct, got)

	// G should return the tracker
	G(ctx).Update(Progress{Action: "test"})
	updates := ct.Updates()
	require.Len(t, updates, 1)
	assert.Equal(t, Action("test"), updates[0].Action)
}

func TestTrackerFunc(t *testing.T) {
	var received Progress
	f := TrackerFunc(func(p Progress) {
		received = p
	})

	f.Update(Progress{Action: "test", Current: 42})
	assert.Equal(t, Action("test"), received.Action)
	assert.Equal(t, int64(42), received.Current)
}

func TestWithLimiterAlwaysPropagatesFirstAndLast(t *testing.T) {
	base := &collectingTracker{}
	limited := WithLimiter(base, rate.NewLimiter(0, 0))

	started := time.Now()
	desc := &ocispec.Descriptor{
		Digest: digest.FromString("first-and-last"),
		Size:   100,
	}

	limited.Update(Progress{Descriptor: desc, Action: ActionUploading, Current: 0, Total: 100, Started: started})
	limited.Update(Progress{Descriptor: desc, Action: ActionUploading, Current: 50, Total: 100, Started: started})
	limited.Update(Progress{Descriptor: desc, Action: ActionUploading, Current: 100, Total: 100, Started: started, Completed: time.Now()})

	updates := base.Updates()
	require.Len(t, updates, 2)
	assert.Equal(t, int64(0), updates[0].Current)
	assert.GreaterOrEqual(t, updates[1].Current, int64(100))
	assert.False(t, updates[1].Completed.IsZero())
}

func TestWithLimiterTracksFirstPerOperation(t *testing.T) {
	base := &collectingTracker{}
	limited := WithLimiter(base, rate.NewLimiter(0, 0))

	started := time.Now()
	desc1 := &ocispec.Descriptor{Digest: digest.FromString("op-1"), Size: 10}
	desc2 := &ocispec.Descriptor{Digest: digest.FromString("op-2"), Size: 20}

	limited.Update(Progress{Descriptor: desc1, Action: ActionDownloading, Current: 0, Total: 10, Started: started})
	limited.Update(Progress{Descriptor: desc1, Action: ActionDownloading, Current: 5, Total: 10, Started: started})
	limited.Update(Progress{Descriptor: desc2, Action: ActionDownloading, Current: 0, Total: 20, Started: started})

	updates := base.Updates()
	require.Len(t, updates, 2)
	assert.Equal(t, desc1.Digest, updates[0].Descriptor.Digest)
	assert.Equal(t, desc2.Digest, updates[1].Descriptor.Digest)
}
