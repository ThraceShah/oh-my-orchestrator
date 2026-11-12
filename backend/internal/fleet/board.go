package fleet

import (
	"context"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
)

// BoardSessions is the control plane's view of work that lives on other
// machines. These sessions have no local runtime, so the local reaper does not
// own them. The placement state is reported separately from display status.
func (s *Service) BoardSessions(ctx context.Context) ([]domain.Session, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	placements, err := s.store.ListPlacements(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Session, 0, len(placements))
	for _, p := range placements {
		id := p.EngineID
		if id == "" {
			id = p.SessionID
		}
		kind := domain.SessionKind(p.Kind)
		if kind == "" {
			kind = domain.KindWorker
		}
		out = append(out, domain.Session{
			SessionRecord: domain.SessionRecord{
				ID:          domain.SessionID(id),
				ProjectID:   domain.ProjectID(p.ProjectID),
				Kind:        kind,
				Harness:     domain.AgentHarness(p.Harness),
				DisplayName: p.DisplayName,
				Activity:    domain.Activity{State: domain.ActivityIdle},
			},
			Status:          domain.StatusIdle,
			KanbanColumn:    domain.KanbanBuilding,
			StatusReadiness: string(p.State),
			OwnerNodeID:     p.NodeID,
			PlacementState:  string(p.State),
		})
	}
	return out, nil
}
