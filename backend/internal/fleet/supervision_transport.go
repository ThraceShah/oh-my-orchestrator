package fleet

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
)

// EventControlRequest requests an operation on the authenticated control connection.
const EventControlRequest = "control-request"

// CommandControlResponse returns a correlated control operation result.
const CommandControlResponse = "control-response"

type controlCalls struct {
	mu      sync.Mutex
	pending map[string]chan json.RawMessage
}

// ControlRequest travels over the authenticated worker dial-in, never a new
// listener. NodeID is supplied by the listener, not trusted from the worker.
type ControlRequest struct {
	Op        string          `json:"op"`
	Payload   json.RawMessage `json:"payload"`
	ExpiresAt time.Time       `json:"expiresAt,omitempty"`
}

// CallControl allows a worker's local daemon to address a global session.
// Requests are bounded and callers receive explicit failure on unavailable
// transport; the notification queue's lossy behavior is never called success.
func (d *Dialer) CallControl(ctx context.Context, op string, payload, result any) error {
	if d == nil || d.control == nil {
		return apierr.New(apierr.KindUnavailable, "FLEET_CONTROL_UNAVAILABLE", "The control connection is not available", nil)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	expires, _ := ctx.Deadline()
	raw, err := json.Marshal(ControlRequest{Op: op, Payload: body, ExpiresAt: expires})
	if err != nil {
		return err
	}
	id := newStreamID()
	ch := make(chan json.RawMessage, 1)
	d.control.mu.Lock()
	if len(d.control.pending) >= 64 {
		d.control.mu.Unlock()
		return apierr.New(apierr.KindUnavailable, "FLEET_CONTROL_BUSY", "Too many requests to the control plane", nil)
	}
	d.control.pending[id] = ch
	d.control.mu.Unlock()
	defer func() { d.control.mu.Lock(); delete(d.control.pending, id); d.control.mu.Unlock() }()
	event := Event{Kind: EventControlRequest, CommandID: id, Payload: raw}
	select {
	case d.notify <- event:
	default:
		return apierr.New(apierr.KindUnavailable, "FLEET_CONTROL_BUSY", "The control request queue is full", nil)
	}
	select {
	case reply := <-ch:
		return DecodeSupervisionReply(reply, result)
	case <-ctx.Done():
		return apierr.New(apierr.KindUnavailable, "FLEET_CONTROL_UNAVAILABLE", "The control plane did not acknowledge the request", nil)
	}
}

func (d *Dialer) acceptControlResponse(cmd Command) bool {
	if cmd.Kind != CommandControlResponse {
		return false
	}
	if d.control == nil {
		return true
	}
	d.control.mu.Lock()
	ch := d.control.pending[cmd.ID]
	d.control.mu.Unlock()
	if ch != nil {
		select {
		case ch <- cmd.Payload:
		default:
		}
	}
	return true
}
