// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package ringbuffer provides a simple ring buffer implementation.
package ringbuffer

type RingBuffer[T any] struct {
	data       []T
	head, tail int
	capacity   int
}

// NewRingBuffer creates a new ring buffer with the given capacity.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
	}
}

// IsEmpty returns true if the ring buffer is empty.
func (r *RingBuffer[T]) IsEmpty() bool {
	return r.head == r.tail
}

// IsFull returns true if the ring buffer is full.
func (r *RingBuffer[T]) IsFull() bool {
	return (r.tail+1)%r.capacity == r.head
}

// Push adds an element to the ring buffer. If the buffer is full, the oldest
// element will be overwritten.
func (r *RingBuffer[T]) Push(item T) {
	if r.IsFull() {
		// When full, move the head to the next position to overwrite the oldest
		// item
		r.head = (r.head + 1) % r.capacity
	}
	r.data[r.tail] = item
	r.tail = (r.tail + 1) % r.capacity
}

// Pop removes and returns the element at the head.
func (r *RingBuffer[T]) Pop() (T, bool) {
	if r.IsEmpty() {
		var zero T
		return zero, false
	}

	item := r.data[r.head]
	r.head = (r.head + 1) % r.capacity
	return item, true
}

// Last returns the element at the tail without removing it
func (r *RingBuffer[T]) Last() (T, bool) {
	if r.IsEmpty() {
		var zero T
		return zero, false
	}

	return r.data[(r.tail-1+r.capacity)%r.capacity], true
}

// Peek returns the element at the head without removing it
func (r *RingBuffer[T]) Peek() (T, bool) {
	if r.IsEmpty() {
		var zero T
		return zero, false
	}

	return r.data[r.head], true
}

// ToSlice returns the elements of the ring buffer in order, from head to tail
func (r *RingBuffer[T]) ToSlice() []T {
	if r.IsEmpty() {
		return nil
	}

	var result []T
	for i := r.head; i != r.tail; i = (i + 1) % r.capacity {
		result = append(result, r.data[i])
	}
	return result
}

// ToReversedSlice returns the elements of the ring buffer in reverse order,
// from tail to head.
func (r *RingBuffer[T]) ToReversedSlice() []T {
	if r.IsEmpty() {
		return nil
	}

	var result []T
	for i := (r.tail - 1 + r.capacity) % r.capacity; i != (r.head-1+r.capacity)%r.capacity; i = (i - 1 + r.capacity) % r.capacity {
		result = append(result, r.data[i])
	}
	return result
}

// Size returns the current size of the ring buffer.
func (r *RingBuffer[T]) Size() int { return len(r.data) }

// Get returns the element at the given index
func (r *RingBuffer[T]) Get(index int) (T, bool) {
	if index < 0 || index >= r.Size() {
		var zero T
		return zero, false
	}

	return r.data[index], true
}
