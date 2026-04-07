// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package nats

import (
	"context"
	"fmt"

	"github.com/cenkalti/backoff/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Client struct {
	config struct {
		natsURL    string
		natsStream string
	}

	Jetstream jetstream.JetStream
}

// ClientOption is a functional option for a Client.
type ClientOption func(*Client)

func NewClient(ctx context.Context, opts ...ClientOption) (*Client, error) {
	client := new(Client)

	for _, opt := range opts {
		opt(client)
	}

	// TODO: validate options?

	if err := client.connect(ctx); err != nil {
		return nil, err
	}

	return client, nil
}

// WithURL sets the NATS server URL.
func WithURL(url string) ClientOption {
	return func(c *Client) {
		c.config.natsURL = url
	}
}

// TODO: WithNatsCredentials

// WithStream sets the NATS stream to subscribe to.
func WithStream(stream string) ClientOption {
	return func(c *Client) {
		c.config.natsStream = stream
	}
}

// init establishes the connection to the NATS server and checks that the
// configured stream exists.
func (c *Client) connect(ctx context.Context) error {
	// set up connection
	natsOpts := []nats.Option{
		// nats.UserInfo(s.config.NatsUser, s.config.NatsPassword),
		nats.MaxReconnects(-1),
		// TODO: can we override reconnect behavior?
	}

	nc, err := backoff.Retry(ctx, func() (*nats.Conn, error) {
		return nats.Connect(c.config.natsURL, natsOpts...)
	}, backoff.WithBackOff(backoff.NewExponentialBackOff()))
	if err != nil {
		return fmt.Errorf("connecting to NATS server: %w", err)
	}

	// set up jetstream
	c.Jetstream, err = jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("establishing NATS connection: %w", err)
	}

	// check that the stream exists
	if _, err := c.Jetstream.Stream(ctx, c.config.natsStream); err != nil {
		return fmt.Errorf("fetching stream %s: %w", c.config.natsStream, err)
	}

	return nil
}

// Close drains all subscriptions and publishers started using this client and
// blocks until the connection has closed.
func (c *Client) Close() error {
	doneCh := make(chan struct{}, 1)
	c.Jetstream.Conn().Opts.ClosedCB = func(*nats.Conn) {
		close(doneCh)
	}
	if err := c.Jetstream.Conn().Drain(); err != nil {
		return fmt.Errorf("draining connection: %w", err)
	}
	<-doneCh
	return nil
}
