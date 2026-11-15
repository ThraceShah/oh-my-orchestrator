package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
)

// BoardSessions reads the owning worker's actual derived read model. Failed
// observations are absence of knowledge, never proof of idle work or death.
// No display statuses are persisted in the fleet placement table.
func (s *Service) BoardSessions(ctx context.Context) ([]domain.Session, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	placements, err := s.store.ListPlacements(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := make([]domain.Session, len(placements))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, p := range placements {
		out[i] = unobservedSession(p)
		wg.Add(1)
		go func(i int, p SessionPlacement) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			var session domain.Session
			if err := s.supervision(ctx, p, "session_get", nil, &session); err == nil {
				out[i] = s.canonicalSession(p, session)
			}
		}(i, p)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil && err != context.DeadlineExceeded {
		return nil, err
	}
	return out, nil
}

func unobservedSession(p SessionPlacement) domain.Session {
	session := domain.Session{
		SessionRecord:   domain.SessionRecord{ID: domain.SessionID(p.SessionID), ProjectID: domain.ProjectID(p.ProjectID), Kind: domain.SessionKind(p.Kind), Harness: domain.AgentHarness(p.Harness), Mode: domain.SessionMode(p.Mode), DisplayName: p.DisplayName},
		ParentSessionID: domain.SessionID(p.ParentID), OwnerNodeID: p.NodeID, PlacementState: string(p.State), StatusReadiness: "unavailable", Status: domain.StatusNoSignal,
	}
	if p.State == PlacementAssigned || p.State == PlacementWaking || p.State == PlacementStarting {
		session.StatusReadiness = "checking"
		session.ProvisionState = domain.SessionProvisionProvisioning
	}
	// A successful remote RequestKill records terminal intent durably. This
	// survives an offline observation; connectivity alone never sets it.
	if p.State == PlacementStopped {
		session.IsTerminated = true
		session.Status = domain.StatusTerminated
		session.KanbanColumn = domain.KanbanArchive
	}
	if p.State == PlacementFailed {
		session.ProvisionState = domain.SessionProvisionFailed
		session.ProvisionError = placementFailureReason(p.Detail)
	}
	return session
}
