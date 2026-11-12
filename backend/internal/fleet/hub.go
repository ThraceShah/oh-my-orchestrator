package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Command is one instruction the control plane sends to a connected worker.
type Command struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	CommandSpawn   = "spawn"
	CommandStop    = "stop"
	CommandMessage = "message"
	CommandWake    = "wake"
	CommandPower   = "power"
)

// Event is something a worker reports back. Activity and chat facts stay in
// the payload; the hub does not interpret display status.
type Event struct {
	NodeID    string          `json:"nodeId"`
	SessionID string          `json:"sessionId,omitempty"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	At        time.Time       `json:"at"`
}

const (
	EventStarted = "started"
	EventFailed  = "failed"
	EventOutput  = "output"
	EventChat    = "chat"
	EventPower   = "power"
)

// Conn is one dialed worker. Send must be safe for one writer at a time; Hub
// serializes it.
type Conn interface {
	Send(context.Context, Command) error
	Close() error
}

// Hub tracks which enrolled nodes currently have a dial-in connection.
type Hub struct {
	mu     sync.Mutex
	nodes  map[string]*link
	events chan Event
	terms  map[string]chan TerminalMessage
}

type link struct {
	conn   Conn
	cancel context.CancelFunc
}

func NewHub() *Hub {
	return &Hub{nodes: map[string]*link{}, events: make(chan Event, 64), terms: map[string]chan TerminalMessage{}}
}

// Events is the control plane's inbound stream. It is never closed.
func (h *Hub) Events() <-chan Event { return h.events }

// Attach registers a connection. A newer connection for the same node replaces
// the old one; the old one is closed.
func (h *Hub) Attach(nodeID string, conn Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.nodes[nodeID]; ok {
		old.cancel()
		_ = old.conn.Close()
	}
	_, cancel := context.WithCancel(context.Background())
	h.nodes[nodeID] = &link{conn: conn, cancel: cancel}
}

// Detach removes a connection only when it is still the current one.
func (h *Hub) Detach(nodeID string, conn Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.nodes[nodeID]; ok && cur.conn == conn {
		cur.cancel()
		delete(h.nodes, nodeID)
	}
}

func (h *Hub) Connected(nodeID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.nodes[nodeID]
	return ok
}

// Dispatch sends one command to the node that owns it. A missing connection is
// an error; callers record placement and do not retarget the session.
func (h *Hub) Dispatch(ctx context.Context, nodeID string, cmd Command) error {
	h.mu.Lock()
	cur, ok := h.nodes[nodeID]
	h.mu.Unlock()
	if !ok {
		return errors.New("fleet node is not connected")
	}
	return cur.conn.Send(ctx, cmd)
}

// SubscribeTerminal returns the worker's frames for one stream. Cancel
// removes it and closes the channel.
func (h *Hub) SubscribeTerminal(streamID string) (<-chan TerminalMessage, func()) {
	ch := make(chan TerminalMessage, 64)
	h.mu.Lock()
	h.terms[streamID] = ch
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if h.terms[streamID] == ch {
				delete(h.terms, streamID)
			}
			h.mu.Unlock()
			close(ch)
		})
	}
}

// DeliverTerminal forwards one worker frame to the client waiting on its stream.
// A frame for an unknown stream is ignored. A full queue drops the frame rather
// than stalling the worker connection.
func (h *Hub) DeliverTerminal(msg TerminalMessage) {
	h.mu.Lock()
	ch := h.terms[msg.StreamID]
	h.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

// SendTerminal delivers one client frame to the node that owns the stream.
func (h *Hub) SendTerminal(ctx context.Context, nodeID string, msg TerminalMessage) error {
	h.mu.Lock()
	cur, ok := h.nodes[nodeID]
	h.mu.Unlock()
	if !ok {
		return errors.New("fleet node is not connected")
	}
	sender, ok := cur.conn.(interface {
		SendTerminal(context.Context, TerminalMessage) error
	})
	if !ok {
		return errors.New("fleet node cannot carry a terminal")
	}
	return sender.SendTerminal(ctx, msg)
}

// Publish queues an event from a worker. A full buffer drops the event rather
// than blocking the connection; durable facts are written by the caller before
// publishing when they must not be lost.
func (h *Hub) Publish(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	select {
	case h.events <- ev:
	default:
	}
}
