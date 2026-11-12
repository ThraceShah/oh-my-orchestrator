package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Service is the control-plane use of the fleet: enroll, observe, place, and
// ask a connected worker to run what was placed. It does not launch agents.
type Service struct {
	store *Store
	hub   *Hub
	now   func() time.Time
}

func NewService(store *Store, hub *Hub) *Service {
	return &Service{store: store, hub: hub, now: time.Now}
}

func (s *Service) Enroll(ctx context.Context, n Node) (Enrollment, error) {
	return s.store.Enroll(ctx, n)
}

func (s *Service) Nodes(ctx context.Context) ([]Node, error) {
	nodes, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		if s.hub != nil && s.hub.Connected(nodes[i].ID) {
			nodes[i].Presence = PresenceOnline
		}
	}
	return nodes, nil
}

func (s *Service) Heartbeat(ctx context.Context, nodeID string, caps []Capability) error {
	return s.store.Heartbeat(ctx, nodeID, caps)
}

// ObserveOffline marks stale nodes and the placements they own. Session
// termination is intentionally not part of this result.
func (s *Service) ObserveOffline(ctx context.Context) ([]string, error) {
	return s.store.Expire(ctx, DefaultOfflineAfter)
}

// PlaceAndDispatch records a placement before asking the worker to start.
// Failure leaves the placement in failed and does not select another node.
func (s *Service) PlaceAndDispatch(ctx context.Context, req Request, spawn SpawnRequest) (Decision, error) {
	nodes, err := s.candidates(ctx)
	if err != nil {
		return Decision{}, err
	}
	decision, err := Place(req, nodes)
	if err != nil {
		return Decision{}, err
	}
	placement := SessionPlacement{
		SessionID: req.SessionID, NodeID: decision.NodeID, State: decision.State, UpdatedAt: s.now(),
	}
	if err := s.store.PutPlacement(ctx, placement); err != nil {
		return Decision{}, err
	}
	if decision.State == PlacementOffline {
		return decision, nil
	}
	if decision.Wake {
		if err := s.wake(ctx, decision); err != nil {
			_ = s.fail(ctx, req.SessionID, decision.NodeID, err)
			return Decision{}, err
		}
		return decision, nil
	}
	spawn.Checkout = decision.Checkout
	if err := s.dispatchSpawn(ctx, decision.NodeID, spawn); err != nil {
		_ = s.fail(ctx, req.SessionID, decision.NodeID, err)
		return Decision{}, err
	}
	placement.State = PlacementStarting
	_ = s.store.PutPlacement(ctx, placement)
	decision.State = PlacementStarting
	return decision, nil
}

func (s *Service) Placement(ctx context.Context, sessionID string) (SessionPlacement, bool, error) {
	return s.store.Placement(ctx, sessionID)
}

// NoteStarted records that the owning worker has the session running.
// engineID is the id its local engine assigned; empty keeps the previous one.
func (s *Service) NoteStarted(ctx context.Context, sessionID, nodeID, engineID string) error {
	return s.store.PutPlacement(ctx, SessionPlacement{SessionID: sessionID, NodeID: nodeID, State: PlacementRunning, EngineID: engineID, UpdatedAt: s.now()})
}

// NoteFailed records a start failure without moving the session.
func (s *Service) NoteFailed(ctx context.Context, sessionID, nodeID, detail string) error {
	return s.store.PutPlacement(ctx, SessionPlacement{SessionID: sessionID, NodeID: nodeID, State: PlacementFailed, Detail: detail, UpdatedAt: s.now()})
}

func (s *Service) candidates(ctx context.Context) ([]Candidate, error) {
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(nodes))
	for _, n := range nodes {
		inUse, err := s.store.CountActive(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Candidate{Node: n, InUse: inUse})
	}
	return out, nil
}

func (s *Service) dispatchSpawn(ctx context.Context, nodeID string, spawn SpawnRequest) error {
	if s.hub == nil {
		return fmt.Errorf("fleet node %s is not connected", nodeID)
	}
	body, err := json.Marshal(spawn)
	if err != nil {
		return err
	}
	return s.hub.Dispatch(ctx, nodeID, Command{Kind: CommandSpawn, Payload: body})
}

func (s *Service) wake(ctx context.Context, decision Decision) error {
	if s.hub == nil {
		return fmt.Errorf("power broker %s is not connected", decision.BrokerID)
	}
	body, _ := json.Marshal(map[string]string{"nodeId": decision.NodeID, "action": "wake"})
	return s.hub.Dispatch(ctx, decision.BrokerID, Command{Kind: CommandPower, Payload: body})
}

func (s *Service) fail(ctx context.Context, sessionID, nodeID string, cause error) error {
	return s.NoteFailed(ctx, sessionID, nodeID, cause.Error())
}
