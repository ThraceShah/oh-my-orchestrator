package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"
)

// Dialer is the worker side of the fleet connection. It keeps retrying until
// ctx ends. Commands are handed to Handler one at a time.
type Dialer struct {
	URL      string
	NodeID   string
	Token    string
	Identity string
	Caps     func() []Capability
	Handler  func(context.Context, Command) Event
	Interval time.Duration
}

func (d *Dialer) Run(ctx context.Context) error {
	if d.URL == "" || d.NodeID == "" || d.Token == "" {
		return errors.New("fleet dialer needs a url, node id, and token")
	}
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	var delay time.Duration
	for {
		if err := d.session(ctx, interval); err != nil && ctx.Err() == nil {
			delay = nextDelay(delay)
		} else {
			delay = 0
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *Dialer) session(ctx context.Context, interval time.Duration) error {
	conn, _, err := websocket.Dial(ctx, d.URL, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	hello, _ := json.Marshal(Hello{NodeID: d.NodeID, Token: d.Token, Identity: d.Identity, Caps: d.capabilities()})
	if err := writeFrame(ctx, conn, Frame{Type: FrameHello, Payload: hello}); err != nil {
		return err
	}
	var accepted Frame
	if err := readFrame(ctx, conn, &accepted); err != nil {
		return err
	}
	if accepted.Type != FrameAccepted {
		return errors.New("fleet control plane rejected the node")
	}
	beat := time.NewTicker(interval)
	defer beat.Stop()
	errCh := make(chan error, 1)
	go func() {
		for {
			var frame Frame
			if err := readFrame(ctx, conn, &frame); err != nil {
				errCh <- err
				return
			}
			if frame.Type != FrameCommand || frame.Command == nil || d.Handler == nil {
				continue
			}
			ev := d.Handler(ctx, *frame.Command)
			ev.NodeID = d.NodeID
			if err := writeFrame(ctx, conn, Frame{Type: FrameEvent, Event: &ev}); err != nil {
				errCh <- err
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-beat.C:
			if err := writeFrame(ctx, conn, Frame{Type: FrameHeartbeat, NodeID: d.NodeID, Caps: d.capabilities()}); err != nil {
				return err
			}
		}
	}
}

func (d *Dialer) capabilities() []Capability {
	if d.Caps == nil {
		return nil
	}
	return d.Caps()
}

func nextDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return time.Second
	}
	if current >= 30*time.Second {
		return 30 * time.Second
	}
	return current * 2
}

// PowerRequest is the only thing a broker is asked to do. The command strings
// live in node configuration and are executed by the deployment's broker
// process; this package does not know how any machine powers on.
type PowerRequest struct {
	NodeID  string `json:"nodeId"`
	Action  string `json:"action"`
	Command string `json:"command"`
}

// DecodePower reads a power command payload.
func DecodePower(cmd Command) (PowerRequest, error) {
	var req PowerRequest
	if cmd.Kind != CommandPower {
		return req, errors.New("not a power command")
	}
	err := json.Unmarshal(cmd.Payload, &req)
	return req, err
}
