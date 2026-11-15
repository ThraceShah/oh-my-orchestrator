package fleet

import (
	"errors"
	"fmt"
	"strings"
)

// Request is what the scheduler needs to place one session. It carries no
// host names and no machine-specific defaults.
type Request struct {
	SessionID         string
	Kind              string
	Harness           string
	Mode              string
	RequireFullLaunch bool
	Platform          string
	// NodeID is an explicit placement. Empty means choose.
	NodeID string
	// HomeNode and HomeCheckout pin an orchestrator session. Both are required
	// together for that kind; neither is a product-wide default.
	HomeNode     string
	HomeCheckout string
	// StickyNode is the node that already holds this session's directory.
	StickyNode string
}

// Candidate is one node the scheduler may look at, with how many sessions it
// already owns.
type Candidate struct {
	Node  Node
	InUse int
}

// Decision is the placement to persist before any remote process starts.
type Decision struct {
	NodeID   string
	State    Placement
	Checkout string
	Wake     bool
	BrokerID string
}

var (
	// ErrNoNode reports that no candidate matches the request.
	ErrNoNode = errors.New("no fleet node matches the session")
	// ErrNodeUnavailable describes an explicitly selected worker constraint.
	ErrNodeUnavailable = errors.New("the requested fleet node cannot take the session")
	// ErrHomeRequired reports incomplete legacy orchestrator home configuration.
	ErrHomeRequired = errors.New("an orchestrator session needs a project home node and checkout")
	// ErrControlExcluded prevents placing subordinate sessions on the control node.
	ErrControlExcluded = errors.New("the control node does not run sessions")
)

// Place chooses where a session lives. Orchestrator sessions ignore the
// general rules and stay on their project home. Worker sessions prefer the
// node that already has their directory, then an explicit node, then an
// online always-on worker. On-demand nodes are never chosen automatically.
func Place(req Request, nodes []Candidate) (Decision, error) {
	if req.Kind == "orchestrator" {
		return placeOrchestrator(req, nodes)
	}
	byID := index(nodes)
	if req.StickyNode != "" {
		n, ok := byID[req.StickyNode]
		if !ok {
			return Decision{}, fmt.Errorf("%w: sticky node %s is not enrolled", ErrNodeUnavailable, req.StickyNode)
		}
		if n.Node.Role != RoleWorker {
			return Decision{}, ErrControlExcluded
		}
		return Decision{NodeID: n.Node.ID, State: stateFor(n.Node)}, nil
	}
	if req.NodeID != "" {
		n, ok := byID[req.NodeID]
		if !ok {
			return Decision{}, fmt.Errorf("%w: node %s is not enrolled", ErrNodeUnavailable, req.NodeID)
		}
		if err := eligible(n, req, true); err != nil {
			return Decision{}, err
		}
		d := Decision{NodeID: n.Node.ID, State: stateFor(n.Node)}
		if n.Node.Availability == OnDemand && n.Node.Presence != PresenceOnline {
			d.State = PlacementWaking
			d.Wake = true
			d.BrokerID = n.Node.PowerBrokerNode
		}
		return d, nil
	}
	var best *Candidate
	for i := range nodes {
		n := &nodes[i]
		if err := eligible(*n, req, false); err != nil {
			continue
		}
		if best == nil || n.InUse < best.InUse {
			best = n
		}
	}
	if best == nil {
		return Decision{}, ErrNoNode
	}
	return Decision{NodeID: best.Node.ID, State: PlacementAssigned}, nil
}

func placeOrchestrator(req Request, nodes []Candidate) (Decision, error) {
	if strings.TrimSpace(req.HomeNode) == "" || strings.TrimSpace(req.HomeCheckout) == "" {
		return Decision{}, ErrHomeRequired
	}
	n, ok := index(nodes)[req.HomeNode]
	if !ok {
		return Decision{}, fmt.Errorf("%w: home node %s is not enrolled", ErrNodeUnavailable, req.HomeNode)
	}
	if n.Node.Role != RoleWorker {
		return Decision{}, ErrControlExcluded
	}
	if err := harnessReady(n.Node, req.Harness); err != nil {
		return Decision{}, err
	}
	state := PlacementAssigned
	if n.Node.Presence != PresenceOnline {
		state = PlacementOffline
	}
	return Decision{NodeID: n.Node.ID, State: state, Checkout: req.HomeCheckout}, nil
}

// eligible reports why a node cannot take a worker session. allowDemand is
// set only for an explicit request.
func eligible(c Candidate, req Request, allowDemand bool) error {
	n := c.Node
	if n.Role != RoleWorker {
		return ErrControlExcluded
	}
	if !allowDemand && n.Availability != AlwaysOn {
		return fmt.Errorf("%w: on-demand nodes are not chosen automatically", ErrNodeUnavailable)
	}
	if n.Presence != PresenceOnline && (!allowDemand || n.Availability != OnDemand) {
		return fmt.Errorf("%w: node is offline", ErrNodeUnavailable)
	}
	if req.Platform != "" && !hasPlatform(n, req.Platform) {
		return fmt.Errorf("%w: platform %s is not offered", ErrNodeUnavailable, req.Platform)
	}
	if c.InUse >= n.Capacity {
		return fmt.Errorf("%w: node is at capacity", ErrNodeUnavailable)
	}
	if err := harnessReady(n, req.Harness); err != nil {
		return err
	}
	if req.RequireFullLaunch {
		supported := false
		for _, capability := range n.Capabilities {
			if capability.Harness == req.Harness && capability.LaunchVersion >= 1 {
				supported = true
				break
			}
		}
		if !supported {
			return fmt.Errorf("%w: worker must be upgraded to support complete launch parameters", ErrNodeUnavailable)
		}
	}
	if req.Mode != "" {
		for _, capability := range n.Capabilities {
			if capability.Harness == req.Harness && len(capability.SupportedModes) > 0 {
				for _, mode := range capability.SupportedModes {
					if mode == req.Mode {
						return nil
					}
				}
				return fmt.Errorf("%w: harness %s does not support %s", ErrNodeUnavailable, req.Harness, req.Mode)
			}
		}
	}
	return nil
}

func harnessReady(n Node, harness string) error {
	if harness == "" {
		return nil
	}
	for _, capability := range n.Capabilities {
		if capability.Harness == harness && capabilityCanLaunch(capability) {
			return nil
		}
	}
	return fmt.Errorf("%w: harness %s is not logged in", ErrNodeUnavailable, harness)
}

func hasPlatform(n Node, want string) bool {
	for _, p := range n.Platforms {
		if strings.EqualFold(p, want) {
			return true
		}
	}
	return false
}

func stateFor(n Node) Placement {
	if n.Presence == PresenceOnline {
		return PlacementAssigned
	}
	return PlacementOffline
}

func index(nodes []Candidate) map[string]Candidate {
	out := make(map[string]Candidate, len(nodes))
	for _, n := range nodes {
		out[n.Node.ID] = n
	}
	return out
}
