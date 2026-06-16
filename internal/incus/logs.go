package incus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// ConsoleLog returns the VM's serial console ring buffer (a point-in-time snapshot,
// not a live stream). Useful for seeing boot output when a VM won't come up.
func (c *Client) ConsoleLog(name string) (string, error) {
	rc, err := c.server.GetInstanceConsoleLog(name, nil)
	if err != nil {
		return "", fmt.Errorf("reading console log for %q: %w", name, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("reading console log for %q: %w", name, err)
	}
	return string(b), nil
}

// CloudInitStatus runs `cloud-init status --long` in the guest and returns its
// output. Requires the guest agent (so gate callers on VM.AgentReady); the command's
// own non-zero exit (e.g. cloud-init "error" state) is reported in the captured text,
// not as an error.
func (c *Client) CloudInitStatus(ctx context.Context, name string) (string, error) {
	// The client mirrors stdout and stderr on separate goroutines, so give each its
	// own buffer (a shared one would be written concurrently → data race) and wait on
	// DataDone before reading: op completion does not guarantee the copies have flushed.
	var stdout, stderr bytes.Buffer
	dataDone := make(chan bool)
	op, err := c.server.ExecInstance(name, api.InstanceExecPost{
		Command:   []string{"cloud-init", "status", "--long"},
		WaitForWS: true,
	}, &incusclient.InstanceExecArgs{Stdout: &stdout, Stderr: &stderr, DataDone: dataDone})
	if err != nil {
		return "", fmt.Errorf("cloud-init status for %q: %w", name, err)
	}
	waitErr := waitOp(ctx, op)
	<-dataDone // the mirror goroutines finish once the op completes or is cancelled

	out := strings.TrimSpace(stdout.String())
	if errText := strings.TrimSpace(stderr.String()); errText != "" {
		if out != "" {
			out += "\n"
		}
		out += errText
	}
	if waitErr != nil {
		return out, fmt.Errorf("cloud-init status for %q: %w", name, waitErr)
	}
	if out == "" {
		out = "(no output — is cloud-init installed in this image?)"
	}
	return out, nil
}
