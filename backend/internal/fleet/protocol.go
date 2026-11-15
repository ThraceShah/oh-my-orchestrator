package fleet

import "encoding/json"

// Frame is one message on the worker dial-in socket. The control plane never
// dials out, so every frame arrives on a connection the worker opened.
type Frame struct {
	Type     string           `json:"type"`
	NodeID   string           `json:"nodeId,omitempty"`
	Command  *Command         `json:"command,omitempty"`
	Event    *Event           `json:"event,omitempty"`
	Terminal *TerminalMessage `json:"terminal,omitempty"`
	Caps     []Capability     `json:"capabilities,omitempty"`
	Payload  json.RawMessage  `json:"payload,omitempty"`
}

// Frame types distinguish connection, execution, and terminal traffic.
const (
	FrameHello     = "hello"
	FrameHeartbeat = "heartbeat"
	FrameCommand   = "command"
	FrameEvent     = "event"
	FrameTerminal  = "terminal"
	FrameAccepted  = "accepted"
	FrameRejected  = "rejected"
)

// TerminalMessage is one frame of a terminal stream. The control plane relays
// it to the node that owns the session; it does not interpret the bytes.
type TerminalMessage struct {
	StreamID  string `json:"streamId"`
	Op        string `json:"op"`
	SessionID string `json:"sessionId,omitempty"`
	Data      string `json:"data,omitempty"`
	Cols      uint16 `json:"cols,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Hello is the first frame a worker sends after the socket opens.
type Hello struct {
	NodeID   string       `json:"nodeId"`
	Token    string       `json:"token"`
	Identity string       `json:"identity,omitempty"`
	Caps     []Capability `json:"capabilities,omitempty"`
}
