package fleet

import (
	"context"
	"encoding/json"
	"fmt"
)

// ResumeNode is called after a new authenticated connection is attached. A
// missing acknowledgement is retried with the same worker idempotency key.
// Already-started placements are never started again, even after a reconnect.
func (s *Service) ResumeNode(ctx context.Context, nodeID string) error {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if err := s.store.resetUnacknowledged(ctx, nodeID); err != nil {
		return err
	}
	return s.resumePending(ctx, nodeID)
}

// ResumePending sends requests that have not yet reached the current link.
// Heartbeats may call this without resending requests awaiting acknowledgement.
func (s *Service) ResumePending(ctx context.Context, nodeID string) error {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	return s.resumePending(ctx, nodeID)
}
func (s *Service) resumePending(ctx context.Context, nodeID string) error {
	placements, err := s.store.ListPlacements(ctx)
	if err != nil {
		return err
	}
	for _, p := range placements {
		if p.NodeID != nodeID || p.EngineID != "" || p.DispatchState != "pending" || len(p.Payload) == 0 {
			continue
		}
		if p.State == PlacementFailed || p.State == PlacementStopped || p.State == Placement("terminated") {
			continue
		}
		if err := s.dispatchPending(ctx, p); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) dispatchPending(ctx context.Context, p SessionPlacement) error {
	claimed, err := s.store.claimLaunch(ctx, p.SessionID)
	if err != nil || !claimed {
		return err
	}
	var request SpawnRequest
	if err := json.Unmarshal(p.Payload, &request); err != nil {
		_ = s.fail(ctx, p.SessionID, p.NodeID, err)
		return fmt.Errorf("decode fleet launch: %w", err)
	}
	if request.Launch != nil {
		node, found, err := s.store.Node(ctx, p.NodeID)
		supported := false
		for _, capability := range node.Capabilities {
			if capability.Harness == p.Harness && capability.LaunchVersion >= 1 {
				supported = true
				break
			}
		}
		if err != nil || !found || !supported {
			if err == nil {
				err = fmt.Errorf("worker must be upgraded to support complete launch parameters")
			}
			_ = s.store.markLaunch(ctx, p.SessionID, "pending")
			p.State, p.Detail = PlacementOffline, err.Error()
			_ = s.store.PutPlacement(ctx, p)
			return err
		}
	}
	// Mark starting before sending: the worker may acknowledge synchronously.
	p.State = PlacementStarting
	p.Detail = ""
	if err := s.store.PutPlacement(ctx, p); err != nil {
		_ = s.store.markLaunch(ctx, p.SessionID, "pending")
		return err
	}
	if err := s.dispatchSpawn(ctx, p.NodeID, request); err != nil {
		_ = s.store.markLaunch(ctx, p.SessionID, "pending")
		p.State = PlacementOffline
		p.Detail = err.Error()
		_ = s.store.PutPlacement(ctx, p)
		return err
	}
	return nil
}
