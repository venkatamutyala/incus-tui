// Package incus is the service layer for incus-tui: a thin, TUI-agnostic wrapper
// over the official Incus v7 Go client. It exposes flattened value types (no
// Bubble Tea imports here) so the UI layer can stay free of Incus API details and
// so this layer can be integration-tested against a live daemon on its own.
package incus

import (
	"context"
	"fmt"
	"sync"

	incusclient "github.com/lxc/incus/v7/client"
)

// Image server used both for browsing images and as the source when creating a VM.
// Kept as a single constant so the browse path and the create path can never drift.
const (
	ImageServerURL = "https://images.linuxcontainers.org"
	ImageProtocol  = "simplestreams"
)

// Client wraps a connection to the local Incus daemon plus a lazily-established
// connection to the public image server.
type Client struct {
	server incusclient.InstanceServer

	mu     sync.Mutex // guards the lazy images cache against a quit-during-image-load race
	images incusclient.ImageServer
}

// Connect dials the local Incus daemon over its unix socket. The empty path lets
// the client resolve the socket from INCUS_SOCKET / INCUS_DIR / the default paths.
func Connect() (*Client, error) {
	srv, err := incusclient.ConnectIncusUnix("", nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to local incus daemon: %w", err)
	}
	return &Client{server: srv}, nil
}

// imageServer returns a cached connection to the public simplestreams image server.
func (c *Client) imageServer() (incusclient.ImageServer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.images != nil {
		return c.images, nil
	}
	is, err := incusclient.ConnectSimpleStreams(ImageServerURL, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to image server: %w", err)
	}
	c.images = is
	return c.images, nil
}

// waitOp waits for an Incus operation, cancelable via ctx. If ctx is cancelled it
// also best-effort cancels the server-side operation so a hung op (e.g. a stuck
// image pull during a launch) doesn't keep running after the user aborts.
func waitOp(ctx context.Context, op incusclient.Operation) error {
	err := op.WaitContext(ctx)
	if err != nil && ctx.Err() != nil {
		_ = op.Cancel()
	}
	return err
}

// Disconnect releases the underlying connections.
func (c *Client) Disconnect() {
	if c.server != nil {
		c.server.Disconnect()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.images != nil {
		c.images.Disconnect()
	}
}
