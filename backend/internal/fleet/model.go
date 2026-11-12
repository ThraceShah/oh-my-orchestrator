// Package fleet places sessions on worker machines without owning their
// execution. A control node keeps the board; workers run the existing session
// engine and dial in. Nothing here names a particular machine.
package fleet

import (
	"errors"
	"strings"
	"time"
)

// Role is what a node does in the fleet. A node has exactly one role;
// power-broker is a capability a worker may also carry.
type Role string

const (
	RoleControl Role = "control"
	RoleWorker  Role = "worker"
)

// Availability decides whether a worker is eligible for automatic placement.
type Availability string

const (
	AlwaysOn Availability = "always-on"
	OnDemand Availability = "on-demand"
)

// Presence is observed from heartbeats. It is not a session status.
type Presence string

const (
	PresenceOnline  Presence = "online"
	PresenceOffline Presence = "offline"
)

// Placement is a durable fact about where a session lives. Display status is
// still derived elsewhere and is never stored here.
type Placement string

const (
	PlacementAssigned Placement = "assigned"
	PlacementWaking   Placement = "waking"
	PlacementStarting Placement = "starting"
	PlacementRunning  Placement = "running"
	PlacementOffline  Placement = "offline"
	PlacementFailed   Placement = "failed"
)

// Node is one enrolled machine.
type Node struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Role             Role         `json:"role"`
	Availability     Availability `json:"availability"`
	Capacity         int          `json:"capacity"`
	Platforms        []string     `json:"platforms,omitempty"`
	PowerBroker      bool         `json:"powerBroker"`
	PowerBrokerNode  string       `json:"powerBrokerNodeId,omitempty"`
	WakeCommand      string       `json:"wakeCommand,omitempty"`
	ShutdownCommand  string       `json:"shutdownCommand,omitempty"`
	ExpectedIdentity string       `json:"expectedIdentity,omitempty"`
	TokenHash        string       `json:"-"`
	Presence         Presence     `json:"presence"`
	LastSeenAt       time.Time    `json:"lastSeenAt,omitempty"`
	Capabilities     []Capability `json:"capabilities,omitempty"`
}

// Capability is one harness the node reports as installed. Credentials never
// leave the node; only whether a login exists is reported.
type Capability struct {
	Harness    string    `json:"harness"`
	LoggedIn   bool      `json:"loggedIn"`
	Version    string    `json:"version,omitempty"`
	ReportedAt time.Time `json:"reportedAt,omitempty"`
}

// SessionPlacement records which node owns a session's directory.
type SessionPlacement struct {
	SessionID string    `json:"sessionId"`
	NodeID    string    `json:"nodeId,omitempty"`
	State     Placement `json:"state"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SpawnRequest is the worker-facing instruction to start a session. The
// worker reuses the existing local session engine; this carries only the
// facts the control plane already decided.
type SpawnRequest struct {
	SessionID   string `json:"sessionId"`
	ProjectID   string `json:"projectId,omitempty"`
	Kind        string `json:"kind"`
	Harness     string `json:"harness,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	ParentID    string `json:"parentSessionId,omitempty"`
	// Checkout is the long-lived directory of an orchestrator session. Worker
	// sessions leave it empty and get a fresh worktree.
	Checkout string `json:"checkout,omitempty"`
}

const (
	DefaultHeartbeatInterval = 15 * time.Second
	DefaultOfflineAfter      = 45 * time.Second
	DefaultWakeTimeout       = 5 * time.Minute
)

func (n Node) Validate() error {
	if strings.TrimSpace(n.ID) == "" {
		return errors.New("fleet node id is required")
	}
	if n.Role != RoleControl && n.Role != RoleWorker {
		return errors.New("fleet node role must be control or worker")
	}
	if n.Role == RoleControl {
		return nil
	}
	if n.Availability != AlwaysOn && n.Availability != OnDemand {
		return errors.New("fleet worker availability must be always-on or on-demand")
	}
	if n.Capacity < 1 {
		return errors.New("fleet worker capacity must be at least 1")
	}
	if n.Availability == OnDemand && strings.TrimSpace(n.PowerBrokerNode) == "" {
		return errors.New("an on-demand node needs a power broker")
	}
	return nil
}
