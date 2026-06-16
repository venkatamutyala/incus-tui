package incus

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

// EventKind classifies an event delivered to the UI.
type EventKind int

const (
	// EventLifecycle signals that an instance changed (created/started/stopped/...);
	// the UI should re-fetch rather than mutate rows directly (avoids lost updates).
	EventLifecycle EventKind = iota
	// EventListenerDown signals the event stream dropped and is being re-established;
	// the UI should show a "reconnecting/stale" indicator.
	EventListenerDown
	// EventListenerUp signals the event stream is healthy again.
	EventListenerUp
)

// Event is a flattened event for the UI layer.
type Event struct {
	Kind     EventKind
	Instance string // instance name (lifecycle events)
	Action   string // e.g. "instance-started"
}

// WatchEvents subscribes to the Incus lifecycle event stream and forwards flattened
// events on out. The Incus EventListener has no auto-reconnect, so this supervises
// it: when the stream dies it emits EventListenerDown, reconnects with capped
// exponential backoff, and emits EventListenerUp on recovery. It returns when done
// is closed. Intended to run in its own goroutine; never mutates UI state.
func (c *Client) WatchEvents(out chan<- Event, done <-chan struct{}) {
	const maxBackoff = 30 * time.Second
	backoff := time.Second

	for {
		select {
		case <-done:
			return
		default:
		}

		listener, err := c.server.GetEvents()
		if err != nil {
			emit(out, done, Event{Kind: EventListenerDown})
			if !sleep(done, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		backoff = time.Second
		// Register the handler before signalling "up" so no lifecycle event can slip
		// through between the badge clearing and the handler being attached.
		_, _ = listener.AddHandler([]string{"lifecycle"}, func(e api.Event) {
			emit(out, done, parseLifecycle(e))
		})
		emit(out, done, Event{Kind: EventListenerUp})

		dead := make(chan struct{})
		go func() {
			_ = listener.Wait()
			close(dead)
		}()

		select {
		case <-done:
			listener.Disconnect()
			return
		case <-dead:
			listener.Disconnect()
			emit(out, done, Event{Kind: EventListenerDown})
			if !sleep(done, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

func parseLifecycle(e api.Event) Event {
	ev := Event{Kind: EventLifecycle}
	var lc api.EventLifecycle
	if err := json.Unmarshal(e.Metadata, &lc); err == nil {
		ev.Action = lc.Action
		ev.Instance = lc.Name
		if ev.Instance == "" {
			ev.Instance = instanceFromSource(lc.Source)
		}
	}
	return ev
}

// instanceFromSource extracts the instance name from a lifecycle Source path such
// as "/1.0/instances/my-vm".
func instanceFromSource(src string) string {
	const marker = "/instances/"
	if i := strings.Index(src, marker); i >= 0 {
		rest := src[i+len(marker):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return ""
}

func emit(out chan<- Event, done <-chan struct{}, ev Event) {
	select {
	case out <- ev:
	case <-done:
	}
}

// sleep waits for d or until done is closed; it returns false if done fired.
func sleep(done <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-done:
		return false
	}
}
