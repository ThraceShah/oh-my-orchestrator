package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Service is the control-plane use of the fleet: enroll, observe, place, and
// ask a connected worker to run what was placed. It does not launch agents.
type Service struct {
	store    *Store
	hub      *Hub
	now      func() time.Time
	launchMu sync.Mutex
}

// NewService connects fleet persistence to the authenticated worker hub.
func NewService(store *Store, hub *Hub) *Service {
	return &Service{store: store, hub: hub, now: time.Now}
}

// Enroll registers a node and issues its one-time dial-in credential.
func (s *Service) Enroll(ctx context.Context, n Node) (Enrollment, error) {
	return s.store.Enroll(ctx, n)
}

// Nodes returns enrollment, observed presence, capabilities, and owned load.
func (s *Service) Nodes(ctx context.Context) ([]Node, error) {
	nodes, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		if s.hub != nil && s.hub.Connected(nodes[i].ID) {
			nodes[i].Presence = PresenceOnline
		}
		nodes[i].InUse, err = s.store.CountActive(ctx, nodes[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// Heartbeat records connectivity without refreshing native probe timestamps.
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
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	req.RequireFullLaunch = spawn.Launch != nil
	if req.Mode == "" {
		req.Mode = spawn.Mode
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return Decision{}, fmt.Errorf("a fleet launch needs a session id")
	}
	spawn.SessionID = req.SessionID
	hash := spawn.RequestHash
	if hash == "" {
		body, err := json.Marshal(spawn)
		if err != nil {
			return Decision{}, err
		}
		sum := sha256.Sum256(body)
		hash = hex.EncodeToString(sum[:])
	}
	if original, found, err := s.store.launchConflict(ctx, req.SessionID, hash); err != nil {
		return Decision{}, err
	} else if found {
		return Decision{NodeID: original.NodeID, State: original.State}, nil
	}
	nodes, err := s.candidates(ctx)
	if err != nil {
		return Decision{}, err
	}
	decision, err := Place(req, nodes)
	if err != nil {
		return Decision{}, err
	}
	spawn.Checkout = decision.Checkout
	spawn.RequestHash = hash
	body, err := json.Marshal(spawn)
	if err != nil {
		return Decision{}, err
	}
	placement := SessionPlacement{
		SessionID: req.SessionID, NodeID: decision.NodeID, State: decision.State, UpdatedAt: s.now(),
		ProjectID: spawn.ProjectID, Kind: spawn.Kind, Harness: spawn.Harness, DisplayName: spawn.DisplayName,
		ParentID: spawn.ParentID, Mode: spawn.Mode, RequestHash: hash, Payload: body, DispatchState: "pending",
	}
	created, err := s.store.SaveLaunch(ctx, placement)
	if err != nil {
		return Decision{}, err
	}
	if !created {
		original, _, err := s.store.launchConflict(ctx, req.SessionID, hash)
		return Decision{NodeID: original.NodeID, State: original.State}, err
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
	if err := s.dispatchPending(ctx, placement); err != nil {
		return Decision{}, err
	}
	decision.State = PlacementStarting
	return decision, nil
}

// Placement reads the durable owner of a global session identity.
func (s *Service) Placement(ctx context.Context, sessionID string) (SessionPlacement, bool, error) {
	return s.store.Placement(ctx, sessionID)
}

// NoteStarted records that the owning worker has the session running.
// engineID and mode are the worker engine's committed identity and interface.
func (s *Service) NoteStarted(ctx context.Context, sessionID, nodeID, engineID string, actualMode ...string) error {
	original, found, err := s.store.Placement(ctx, sessionID)
	if err != nil {
		return err
	}
	if !found || original.NodeID != nodeID {
		return fmt.Errorf("fleet start does not match placement owner")
	}
	if strings.TrimSpace(engineID) == "" {
		return fmt.Errorf("fleet start omitted engine identity")
	}
	if original.State == PlacementStopped {
		return nil
	}
	if original.EngineID != "" && original.EngineID != engineID {
		return fmt.Errorf("fleet start attempted to replace engine identity")
	}
	mode := original.Mode
	if len(actualMode) > 0 && (actualMode[0] == "chat" || actualMode[0] == "tui") {
		mode = actualMode[0]
	}
	return s.store.ackStarted(ctx, sessionID, nodeID, engineID, mode, s.now())
}

// NoteFailed records a start failure without moving the session.
func (s *Service) NoteFailed(ctx context.Context, sessionID, nodeID, detail string) error {
	original, found, err := s.store.PlacementFor(ctx, sessionID)
	if err != nil {
		return err
	}
	if !found || original.NodeID != nodeID {
		return fmt.Errorf("fleet failure does not match placement owner")
	}
	// A failed message or chat operation is not a failed process launch.
	if original.EngineID != "" || original.State == PlacementStopped {
		return nil
	}
	if err := s.store.PutPlacement(ctx, SessionPlacement{SessionID: original.SessionID, NodeID: nodeID, State: PlacementFailed, Detail: detail, UpdatedAt: s.now()}); err != nil {
		return err
	}
	return s.store.markLaunch(ctx, original.SessionID, "failed")
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

// Chat asks the worker that owns a session to run one chat operation. A session
// this fleet does not own is not handled, so the local chat service proceeds.
// The returned JSON is the worker chat service's own response.
func (s *Service) Chat(ctx context.Context, sessionID, op string, payload json.RawMessage) (json.RawMessage, bool, error) {
	if s == nil || s.store == nil || s.hub == nil {
		return nil, false, nil
	}
	placement, ok, err := s.store.Placement(ctx, sessionID)
	if err != nil || !ok {
		return nil, false, err
	}
	engine := placement.EngineID
	if engine == "" {
		engine = placement.SessionID
	}
	body, err := json.Marshal(struct {
		SessionID string          `json:"sessionId"`
		Op        string          `json:"op"`
		Payload   json.RawMessage `json:"payload,omitempty"`
	}{SessionID: engine, Op: op, Payload: payload})
	if err != nil {
		return nil, true, err
	}
	ev, err := s.hub.Call(ctx, placement.NodeID, Command{Kind: CommandChat, Payload: body})
	if err != nil {
		return nil, true, err
	}
	if ev.Kind == EventFailed {
		return nil, true, fmt.Errorf("fleet chat: %s", ev.Payload)
	}
	return canonicalChatSessionIDs(ev.Payload, placement), true, nil
}
