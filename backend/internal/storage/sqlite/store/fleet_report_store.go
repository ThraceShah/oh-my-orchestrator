package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/storage/sqlite/gen"
)

// AcknowledgeFleetReport retires a worker outbox row only after the control
// plane has durably accepted it. Scope by session as well as report identity.
func (s *Store) AcknowledgeFleetReport(ctx context.Context, sessionID domain.SessionID, id string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.qw.AcknowledgeFleetReport(ctx, gen.AcknowledgeFleetReportParams{ID: id, SessionID: string(sessionID), ClaimToken: id, AcknowledgedAt: sql.NullTime{Time: at, Valid: true}})
	return err
}
