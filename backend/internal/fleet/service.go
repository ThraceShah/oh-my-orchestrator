package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
		ProjectID: spawn.ProjectID, Kind: spawn.Kind, Harness: spawn.Harness, DisplayName: spawn.DisplayName,
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
	return s.dispatchPower(ctx, decision.NodeID, decision.BrokerID, "wake")
}

// RequestPower asks a node's broker to run the configured wake or shutdown
// command. Shutdown is refused while the node still owns a session, so a
// directory is not powered off underneath a running agent.
func (s *Service) RequestPower(ctx context.Context, nodeID, action string) error {
	if action != "wake" && action != "shutdown" {
		return fmt.Errorf("unsupported power action %q", action)
	}
	node, ok, err := s.store.Node(ctx, nodeID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: node %s is not enrolled", ErrNodeUnavailable, nodeID)
	}
	if node.Availability != OnDemand {
		return fmt.Errorf("%w: only an on-demand node has a power broker", ErrNodeUnavailable)
	}
	if action == "shutdown" {
		active, err := s.store.CountActive(ctx, nodeID)
		if err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("%w: node %s still has %d session(s)", ErrNodeUnavailable, nodeID, active)
		}
	}
	return s.dispatchPower(ctx, node.ID, node.PowerBrokerNode, action)
}

func (s *Service) dispatchPower(ctx context.Context, nodeID, brokerID, action string) error {
	if s.hub == nil || strings.TrimSpace(brokerID) == "" {
		return fmt.Errorf("power broker %s is not connected", brokerID)
	}
	node, ok, err := s.store.Node(ctx, nodeID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: node %s is not enrolled", ErrNodeUnavailable, nodeID)
	}
	command := node.WakeCommand
	if action == "shutdown" {
		command = node.ShutdownCommand
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("no %s command is configured for %s", action, nodeID)
	}
	body, err := json.Marshal(PowerRequest{NodeID: nodeID, Action: action, Command: command})
	if err != nil {
		return err
	}
	return s.hub.Dispatch(ctx, brokerID, Command{Kind: CommandPower, Payload: body})
}

func (s *Service) fail(ctx context.Context, sessionID, nodeID string, cause error) error {
	return s.NoteFailed(ctx, sessionID, nodeID, cause.Error())
}

// DeliverMessage sends text to the worker that owns a placed session. A session
// this fleet does not own is reported as not handled so the local engine can
// take it. The worker addresses the session by its own engine id.
func (s *Service) DeliverMessage(ctx context.Context, sessionID, message string) (bool, error) {
	if s == nil || s.store == nil {
		return false, nil
	}
	placement, ok, err := s.store.PlacementFor(ctx, sessionID)
	if err != nil || !ok {
		return false, err
	}
	engine := placement.EngineID
	if engine == "" {
		engine = placement.SessionID
	}
	body, err := json.Marshal(map[string]string{"sessionId": engine, "message": message})
	if err != nil {
		return true, err
	}
	return true, s.hub.Dispatch(ctx, placement.NodeID, Command{Kind: CommandMessage, Payload: body})
}
