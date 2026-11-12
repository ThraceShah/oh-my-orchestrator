package fleet

import "encoding/json"

// Frame is one message on the worker dial-in socket. The control plane never
// dials out, so every frame arrives on a connection the worker opened.
type Frame struct {
	Type    string          `json:"type"`
	NodeID  string          `json:"nodeId,omitempty"`
	Command *Command        `json:"command,omitempty"`
	Event   *Event          `json:"event,omitempty"`
	Caps    []Capability    `json:"capabilities,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	FrameHello     = "hello"
	FrameHeartbeat = "heartbeat"
	FrameCommand   = "command"
	FrameEvent     = "event"
	FrameAccepted  = "accepted"
	FrameRejected  = "rejected"
)

// Hello is the first frame a worker sends after the socket opens.
type Hello struct {
	NodeID   string       `json:"nodeId"`
	Token    string       `json:"token"`
	Identity string       `json:"identity,omitempty"`
	Caps     []Capability `json:"capabilities,omitempty"`
}
