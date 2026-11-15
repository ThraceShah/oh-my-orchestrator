package fleet

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
)

// PlacementForNode disambiguates worker-local engine ids by authenticated owner.
func (s *Store) PlacementForNode(ctx context.Context, nodeID, engineID string) (SessionPlacement, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM fleet_session_placements WHERE node_id=? AND engine_id=?`, nodeID, engineID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionPlacement{}, false, nil
	}
	if err != nil {
		return SessionPlacement{}, false, err
	}
	return s.Placement(ctx, id)
}

// ReportInbox is a durable transfer receipt. Display status is never stored.
type ReportInbox struct {
	ID        string
	NodeID    string
	SessionID domain.SessionID
	Report    domain.ReportRecord
}

func transferredReportID(nodeID, id string) string {
	sum := sha256.Sum256([]byte(nodeID + "\x00" + id))
	return "frpt_" + hex.EncodeToString(sum[:])
}

func (s *Store) acceptReport(ctx context.Context, p SessionPlacement, report domain.ReportRecord) error {
	if report.ID == "" || string(report.SessionID) != p.EngineID {
		return fmt.Errorf("report session does not match owning worker")
	}
	if err := domain.ValidateReportContent(report.State, report.Note, report.Message, report.Outputs); err != nil {
		return err
	}
	report.ID = transferredReportID(p.NodeID, report.ID)
	report.SessionID = domain.SessionID(p.SessionID)
	report.ProjectID = domain.ProjectID(p.ProjectID)
	report.DeliveryState = domain.ReportPending
	report.ClaimToken = ""
	report.ClaimedAt = time.Time{}
	report.AcknowledgedAt = time.Time{}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO fleet_supervision_inbox(id,node_id,session_id,kind,payload,received_at) VALUES(?,?,?,'report',?,?) ON CONFLICT(id) DO NOTHING`, report.ID, p.NodeID, p.SessionID, string(raw), s.now().UTC().Format(time.RFC3339Nano))
	return err
}

// PendingReportInbox lists durable receipts awaiting semantic acceptance.
func (s *Store) PendingReportInbox(ctx context.Context) ([]ReportInbox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,node_id,session_id,payload FROM fleet_supervision_inbox WHERE kind='report' AND acknowledged_at='' ORDER BY received_at,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ReportInbox
	for rows.Next() {
		var item ReportInbox
		var payload string
		if err := rows.Scan(&item.ID, &item.NodeID, &item.SessionID, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &item.Report); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AcknowledgeReportInbox records successful semantic delivery of one receipt.
func (s *Store) AcknowledgeReportInbox(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE fleet_supervision_inbox SET acknowledged_at=?,last_error='' WHERE id=? AND acknowledged_at=''`, s.now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// FailReportInbox records a transient delivery failure without losing the receipt.
func (s *Store) FailReportInbox(ctx context.Context, id string, cause error) error {
	_, err := s.db.ExecContext(ctx, `UPDATE fleet_supervision_inbox SET last_error=? WHERE id=? AND acknowledged_at=''`, cause.Error(), id)
	return err
}

// ListProjectReports exposes transfer receipts to the existing report history.
func (s *Store) ListProjectReports(ctx context.Context, projectID domain.ProjectID) ([]domain.ReportRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload,acknowledged_at,last_error FROM fleet_supervision_inbox WHERE kind='report' ORDER BY received_at,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.ReportRecord
	for rows.Next() {
		var raw, acknowledged, lastError string
		if err := rows.Scan(&raw, &acknowledged, &lastError); err != nil {
			return nil, err
		}
		var rec domain.ReportRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, err
		}
		rec.LastError = lastError
		if acknowledged != "" {
			rec.DeliveryState = domain.ReportAcknowledged
			rec.AcknowledgedAt, _ = time.Parse(time.RFC3339Nano, acknowledged)
		}
		if rec.ProjectID == projectID {
			out = append(out, rec)
		}
	}
	return out, rows.Err()
}

// DeliverReports retires durable transfer receipts only after semantic acceptance.
// The stable receipt id must be used as the delivery idempotency key by deliver.
func (s *Service) DeliverReports(ctx context.Context, deliver func(context.Context, SessionPlacement, domain.ReportRecord, string) error) error {
	records, err := s.store.PendingReportInbox(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, item := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Report.AvailableAt.After(s.now()) {
			continue
		}
		placement, found, err := s.store.Placement(ctx, string(item.SessionID))
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if err := deliver(ctx, placement, item.Report, item.ID); err != nil {
			if first == nil {
				first = err
			}
			if saveErr := s.store.FailReportInbox(ctx, item.ID, err); saveErr != nil {
				return saveErr
			}
			continue
		}
		if err := s.store.AcknowledgeReportInbox(ctx, item.ID); err != nil {
			return err
		}
	}
	return first
}

// PullReports transfers before acknowledging. Both halves are independently
// durable, so a socket disconnect or either daemon restart simply replays.
func (s *Service) PullReports(ctx context.Context) error {
	placements, err := s.store.ListPlacements(ctx)
	if err != nil {
		return err
	}
	for _, p := range placements {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.EngineID == "" || s.hub == nil || !s.hub.Connected(p.NodeID) {
			continue
		}
		var reports []domain.ReportRecord
		if err := s.supervision(ctx, p, "session_reports", nil, &reports); err != nil {
			continue
		}
		for _, report := range reports {
			workerID := report.ID
			if err := s.store.acceptReport(ctx, p, report); err != nil {
				return err
			}
			// Lost acknowledgements are safe: acceptance deduplicates by node+report.
			if err := s.supervision(ctx, p, "session_report_ack", workerID, nil); err != nil {
				break
			}
		}
	}
	return nil
}
