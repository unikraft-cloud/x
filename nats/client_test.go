// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package nats

import (
	"context"
	"testing"
	"time"

	nats_server_test "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestNatsClient(t *testing.T) {
	const testTimeout = 3 * time.Second
	const testStream = "TEST"
	const testSubject = "TEST.subject"
	const testData = "hello world"

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()

	// setup internal nats server for testing
	opts := nats_server_test.DefaultTestOptions
	opts.JetStream = true
	ns := nats_server_test.RunServer(&opts)
	t.Cleanup(ns.Shutdown)

	// create client
	nc, err := nats.Connect(ns.ClientURL())
	assert.NilError(t, err)

	// create jetstream client
	js, err := jetstream.New(nc)
	assert.NilError(t, err)

	// create stream for testing
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     testStream,
		Subjects: []string{testSubject},
	})
	assert.NilError(t, err)

	t.Run("Publisher", func(t *testing.T) {
		nc, err := NewClient(
			ctx,
			WithURL(ns.ClientURL()),
			WithStream(testStream),
		)
		assert.NilError(t, err)
		assert.Assert(t, nc.Jetstream.Conn().IsConnected())

		defer nc.Close()

		_, err = nc.Jetstream.Publish(ctx, testSubject, []byte(testData+"1"))
		_, err = nc.Jetstream.Publish(ctx, testSubject, []byte(testData+"2"))
		_, err = nc.Jetstream.Publish(ctx, testSubject, []byte(testData+"3"))
		assert.NilError(t, err)
	})

	t.Run("Consumer", func(t *testing.T) {
		nc, err := NewClient(
			ctx,
			WithURL(ns.ClientURL()),
			WithStream(testStream),
		)
		assert.NilError(t, err)

		defer nc.Close()

		consumer, err := nc.Jetstream.CreateOrUpdateConsumer(ctx,
			testStream,
			jetstream.ConsumerConfig{})
		assert.NilError(t, err)

		msg, err := consumer.Next(jetstream.FetchContext(ctx))
		assert.NilError(t, err)

		assert.Equal(t, msg.Subject(), testSubject)
		assert.Check(t, cmp.Contains(string(msg.Data()), testData))

		err = msg.DoubleAck(ctx)
		assert.NilError(t, err)
	})
}
