package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CommandQuery requests worker discovery without a session.
const CommandQuery = "query"

// NodeSummary is the application-readable fleet contract. Enrollment credentials,
// power shell commands and network identity are deliberately absent.
type NodeSummary struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Role         Role         `json:"role"`
	Availability Availability `json:"availability"`
	Capacity     int          `json:"capacity"`
	InUse        int          `json:"inUse"`
	Platforms    []string     `json:"platforms,omitempty"`
	Presence     Presence     `json:"presence"`
	LastSeenAt   time.Time    `json:"lastSeenAt,omitempty"`
	Capabilities []Capability `json:"capabilities"`
}

// PublicNodes projects enrolled nodes without credentials or power commands.
func PublicNodes(nodes []Node) []NodeSummary {
	out := make([]NodeSummary, 0, len(nodes))
	for _, n := range nodes {
		caps := n.Capabilities
		if caps == nil {
			caps = []Capability{}
		}
		out = append(out, NodeSummary{ID: n.ID, Name: n.Name, Role: n.Role, Availability: n.Availability, Capacity: n.Capacity, InUse: n.InUse, Platforms: n.Platforms, Presence: n.Presence, LastSeenAt: n.LastSeenAt, Capabilities: caps})
	}
	return out
}
func capabilityCanLaunch(capability Capability) bool {
	if capability.Installation == "" && capability.Authentication == "" {
		return capability.LoggedIn
	}
	if capability.Installation != "installed" {
		return false
	}
	switch capability.Authentication {
	case "authorized", "configured", "not_applicable":
		return true
	default:
		return false
	}
}

// NodeQuery addresses a worker-local catalog before session creation.
type NodeQuery struct {
	Operation string `json:"operation"`
	Agent     string `json:"agent,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	Refresh   bool   `json:"refresh,omitempty"`
}

// QueryFunc reads a catalog in the target worker environment.
type QueryFunc func(context.Context, NodeQuery) (json.RawMessage, error)

// WithDiscovery adds pre-session inventory RPC to the same authenticated link
// used for session commands. It never invents a session to look up a model.
func WithDiscovery(inner func(context.Context, Command) Event, query QueryFunc) func(context.Context, Command) Event {
	return func(ctx context.Context, cmd Command) Event {
		if cmd.Kind != CommandQuery {
			return inner(ctx, cmd)
		}
		var request NodeQuery
		if err := json.Unmarshal(cmd.Payload, &request); err != nil {
			return Event{Kind: EventFailed, Payload: jsonRaw(err.Error())}
		}
		if query == nil {
			return Event{Kind: EventFailed, Payload: jsonRaw("worker discovery unavailable")}
		}
		result, err := query(ctx, request)
		if err != nil {
			return Event{Kind: EventFailed, Payload: jsonRaw(err.Error())}
		}
		return Event{Kind: "query", Payload: result}
	}
}

// QueryNode asks one connected worker to read its agent readiness or models.
func (s *Service) QueryNode(ctx context.Context, nodeID string, query NodeQuery) (json.RawMessage, error) {
	if s == nil || s.hub == nil {
		return nil, fmt.Errorf("fleet is unavailable")
	}
	node, found, err := s.store.Node(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if !found || node.Role != RoleWorker {
		return nil, fmt.Errorf("%w: no such worker", ErrNodeUnavailable)
	}
	switch query.Operation {
	case "readiness", "models":
	default:
		return nil, fmt.Errorf("unsupported worker query %q", query.Operation)
	}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	// Native model/auth probes have their own bounds; lost links must not leave
	// an API request waiting forever even if its caller supplied no deadline.
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	event, err := s.hub.Call(call, nodeID, Command{Kind: CommandQuery, Payload: body})
	if err != nil {
		return nil, err
	}
	if event.Kind == EventFailed {
		return nil, fmt.Errorf("worker query failed: %s", event.Payload)
	}
	return event.Payload, nil
}
