package fleet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const launchSchema = `
CREATE TABLE IF NOT EXISTS fleet_launch_requests (
 session_id TEXT PRIMARY KEY, parent_id TEXT NOT NULL DEFAULT '', mode TEXT NOT NULL DEFAULT '',
 request_hash TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '', dispatch_state TEXT NOT NULL DEFAULT 'pending'
);
CREATE TABLE IF NOT EXISTS fleet_capability_details (
 node_id TEXT NOT NULL, harness TEXT NOT NULL, payload TEXT NOT NULL, PRIMARY KEY (node_id, harness)
);
CREATE TABLE IF NOT EXISTS fleet_supervision_inbox (
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL, session_id TEXT NOT NULL, kind TEXT NOT NULL,
 payload TEXT NOT NULL, received_at TEXT NOT NULL, acknowledged_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT ''
);`

// ErrRequestConflict prevents reusing one launch identity for different inputs.
var ErrRequestConflict = errors.New("fleet request id was already used with different launch parameters")

// SaveLaunch commits ownership and the complete request together. A conflicting
// writer must read the original owner; it cannot replace a placement on retry.
func (s *Store) SaveLaunch(ctx context.Context, p SessionPlacement) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = s.now()
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO fleet_session_placements
 (session_id,node_id,state,detail,engine_id,project_id,kind,harness,display_name,updated_at)
 VALUES (?,?,?,?,?,?,?,?,?,?)`, p.SessionID, p.NodeID, string(p.State), p.Detail, p.EngineID, p.ProjectID, p.Kind, p.Harness, p.DisplayName, p.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fleet_launch_requests
 (session_id,parent_id,mode,request_hash,payload,dispatch_state) VALUES (?,?,?,?,?,?)`,
		p.SessionID, p.ParentID, p.Mode, p.RequestHash, string(p.Payload), "pending")
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ackStarted publishes identity, committed mode and acknowledgement atomically.
func (s *Store) ackStarted(ctx context.Context, id, node, engine, mode string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE fleet_session_placements SET state='running',engine_id=?,updated_at=? WHERE session_id=? AND node_id=? AND (engine_id='' OR engine_id=?) AND state!='stopped'`, engine, at.UTC().Format(time.RFC3339Nano), id, node, engine)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fleet_launch_requests (session_id,mode,dispatch_state) VALUES (?,?,'acknowledged') ON CONFLICT(session_id) DO UPDATE SET mode=CASE WHEN excluded.mode='' THEN fleet_launch_requests.mode ELSE excluded.mode END,dispatch_state='acknowledged'`, id, mode)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) hydratePlacement(ctx context.Context, p *SessionPlacement) error {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT parent_id,mode,request_hash,payload,dispatch_state FROM fleet_launch_requests WHERE session_id = ?`, p.SessionID).
		Scan(&p.ParentID, &p.Mode, &p.RequestHash, &payload, &p.DispatchState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	p.Payload = []byte(payload)
	return err
}

func (s *Store) putLaunchMetadata(ctx context.Context, p SessionPlacement) error {
	if p.ParentID == "" && p.Mode == "" && len(p.Payload) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO fleet_launch_requests
 (session_id,parent_id,mode,request_hash,payload,dispatch_state) VALUES (?,?,?,?,?,?)
 ON CONFLICT(session_id) DO UPDATE SET
 parent_id=CASE WHEN excluded.parent_id='' THEN fleet_launch_requests.parent_id ELSE excluded.parent_id END,
 mode=CASE WHEN excluded.mode='' THEN fleet_launch_requests.mode ELSE excluded.mode END`,
		p.SessionID, p.ParentID, p.Mode, p.RequestHash, string(p.Payload), p.DispatchState)
	return err
}

func (s *Store) claimLaunch(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE fleet_launch_requests SET dispatch_state='sent' WHERE session_id=? AND dispatch_state='pending'`, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}
func (s *Store) markLaunch(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE fleet_launch_requests SET dispatch_state=? WHERE session_id=?`, state, id)
	return err
}
func (s *Store) resetUnacknowledged(ctx context.Context, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE fleet_launch_requests SET dispatch_state='pending'
 WHERE dispatch_state='sent' AND session_id IN
 (SELECT session_id FROM fleet_session_placements WHERE node_id=? AND engine_id='' AND state NOT IN ('failed','stopped','terminated'))`, nodeID)
	return err
}
func (s *Store) launchConflict(ctx context.Context, id, hash string) (SessionPlacement, bool, error) {
	original, found, err := s.Placement(ctx, id)
	if err != nil || !found {
		return original, found, err
	}
	if original.RequestHash != hash {
		return original, true, fmt.Errorf("%w: %s", ErrRequestConflict, id)
	}
	return original, true, nil
}
