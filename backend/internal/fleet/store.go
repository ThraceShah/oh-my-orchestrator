package fleet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store persists fleet facts in the same SQLite database as the rest of the
// daemon. It does not derive display status and does not touch session
// termination.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore uses the daemon database for durable fleet facts.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// Migrate creates the fleet tables. It is additive and safe to repeat.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS fleet_nodes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    role TEXT NOT NULL,
    availability TEXT NOT NULL DEFAULT '',
    capacity INTEGER NOT NULL DEFAULT 0,
    platforms TEXT NOT NULL DEFAULT '[]',
    power_broker INTEGER NOT NULL DEFAULT 0,
    power_broker_node_id TEXT NOT NULL DEFAULT '',
    wake_command TEXT NOT NULL DEFAULT '',
    shutdown_command TEXT NOT NULL DEFAULT '',
    expected_identity TEXT NOT NULL DEFAULT '',
    token_hash TEXT NOT NULL DEFAULT '',
    presence TEXT NOT NULL DEFAULT 'offline',
    last_seen_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS fleet_node_capabilities (
    node_id TEXT NOT NULL REFERENCES fleet_nodes(id) ON DELETE CASCADE,
    harness TEXT NOT NULL,
    logged_in INTEGER NOT NULL,
    version TEXT NOT NULL DEFAULT '',
    reported_at TEXT NOT NULL,
    PRIMARY KEY (node_id, harness)
);
CREATE TABLE IF NOT EXISTS fleet_session_placements (
    session_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    engine_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    harness TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	// Tables created before engine_id existed keep working. A duplicate-column
	// error means this database already has the column.
	for _, column := range []string{
		`engine_id TEXT NOT NULL DEFAULT ''`,
		`project_id TEXT NOT NULL DEFAULT ''`,
		`kind TEXT NOT NULL DEFAULT ''`,
		`harness TEXT NOT NULL DEFAULT ''`,
		`display_name TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE fleet_session_placements ADD COLUMN `+column); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, launchSchema)
	return err
}

// Enrollment is the one-time result of adding a node. Token is shown once.
type Enrollment struct {
	Node  Node
	Token string
}

// Enroll persists one validated node and returns its new one-time token.
func (s *Store) Enroll(ctx context.Context, n Node) (Enrollment, error) {
	if err := n.Validate(); err != nil {
		return Enrollment{}, err
	}
	token, hash, err := newToken()
	if err != nil {
		return Enrollment{}, err
	}
	platforms, err := json.Marshal(n.Platforms)
	if err != nil {
		return Enrollment{}, err
	}
	broker := 0
	if n.PowerBroker {
		broker = 1
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO fleet_nodes (
    id, name, role, availability, capacity, platforms, power_broker,
    power_broker_node_id, wake_command, shutdown_command, expected_identity, token_hash, presence
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'offline')`,
		n.ID, n.Name, string(n.Role), string(n.Availability), n.Capacity, string(platforms),
		broker, n.PowerBrokerNode, n.WakeCommand, n.ShutdownCommand, n.ExpectedIdentity, hash)
	if err != nil {
		return Enrollment{}, fmt.Errorf("enroll fleet node: %w", err)
	}
	n.Presence = PresenceOffline
	n.TokenHash = hash
	return Enrollment{Node: n, Token: token}, nil
}

// List reads enrolled nodes and their last reported capabilities.
func (s *Store) List(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, role, availability, capacity, platforms, power_broker,
       power_broker_node_id, wake_command, shutdown_command, expected_identity,
       token_hash, presence, last_seen_at
FROM fleet_nodes ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	caps, err := s.capabilities(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Capabilities = caps[out[i].ID]
	}
	return out, nil
}

// Node finds one enrolled node and its capability observations.
func (s *Store) Node(ctx context.Context, id string) (Node, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, role, availability, capacity, platforms, power_broker,
       power_broker_node_id, wake_command, shutdown_command, expected_identity,
       token_hash, presence, last_seen_at
FROM fleet_nodes WHERE id = ?`, id)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	caps, err := s.capabilities(ctx)
	if err != nil {
		return Node{}, false, err
	}
	n.Capabilities = caps[n.ID]
	return n, true, nil
}

// Authenticate checks a dial-in token and, when the node expects one, the
// network identity observed by the listener.
func (s *Store) Authenticate(ctx context.Context, nodeID, token, observedIdentity string) (Node, error) {
	n, ok, err := s.Node(ctx, nodeID)
	if err != nil {
		return Node{}, err
	}
	if !ok || n.TokenHash == "" || n.TokenHash != hashToken(token) {
		return Node{}, errors.New("fleet credentials were rejected")
	}
	if n.ExpectedIdentity != "" && n.ExpectedIdentity != observedIdentity {
		return Node{}, errors.New("fleet network identity does not match the enrolled node")
	}
	return n, nil
}

// Heartbeat marks a node online and replaces its reported capabilities.
func (s *Store) Heartbeat(ctx context.Context, nodeID string, caps []Capability) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE fleet_nodes SET presence = 'online', last_seen_at = ? WHERE id = ?`, now, nodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("fleet node is not enrolled")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fleet_node_capabilities WHERE node_id = ?`, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fleet_capability_details WHERE node_id = ?`, nodeID); err != nil {
		return err
	}
	for _, capability := range caps {
		logged := 0
		if capability.LoggedIn {
			logged = 1
		}
		reported := now
		if !capability.ReportedAt.IsZero() {
			reported = capability.ReportedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO fleet_node_capabilities (node_id, harness, logged_in, version, reported_at)
VALUES (?, ?, ?, ?, ?)`, nodeID, capability.Harness, logged, capability.Version, reported); err != nil {
			return err
		}
		detail, err := json.Marshal(capability)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_capability_details (node_id,harness,payload) VALUES (?,?,?)`, nodeID, capability.Harness, string(detail)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Expire marks nodes offline when their heartbeat is older than the limit.
// It never changes a session's termination.
func (s *Store) Expire(ctx context.Context, after time.Duration) ([]string, error) {
	nodes, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := s.now().Add(-after)
	var expired []string
	for _, n := range nodes {
		if n.Presence != PresenceOnline || n.LastSeenAt.After(cutoff) {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE fleet_nodes SET presence = 'offline' WHERE id = ?`, n.ID); err != nil {
			return nil, err
		}
		expired = append(expired, n.ID)
	}
	if len(expired) == 0 {
		return nil, nil
	}
	return expired, s.markPlacementsOffline(ctx, expired)
}

func (s *Store) markPlacementsOffline(ctx context.Context, nodeIDs []string) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	for _, id := range nodeIDs {
		if _, err := s.db.ExecContext(ctx, `
UPDATE fleet_session_placements
SET state = 'offline', updated_at = ?
WHERE node_id = ? AND state IN ('assigned', 'starting', 'running', 'waking')`, now, id); err != nil {
			return err
		}
	}
	return nil
}

// PutPlacement updates ownership facts while retaining existing launch metadata.
func (s *Store) PutPlacement(ctx context.Context, p SessionPlacement) error {
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = s.now()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO fleet_session_placements (
    session_id, node_id, state, detail, engine_id, project_id, kind, harness, display_name, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    node_id = excluded.node_id,
    state = excluded.state,
    detail = excluded.detail,
    engine_id = CASE WHEN excluded.engine_id = '' THEN fleet_session_placements.engine_id ELSE excluded.engine_id END,
    project_id = CASE WHEN excluded.project_id = '' THEN fleet_session_placements.project_id ELSE excluded.project_id END,
    kind = CASE WHEN excluded.kind = '' THEN fleet_session_placements.kind ELSE excluded.kind END,
    harness = CASE WHEN excluded.harness = '' THEN fleet_session_placements.harness ELSE excluded.harness END,
    display_name = CASE WHEN excluded.display_name = '' THEN fleet_session_placements.display_name ELSE excluded.display_name END,
    updated_at = excluded.updated_at`,
		p.SessionID, p.NodeID, string(p.State), p.Detail, p.EngineID, p.ProjectID, p.Kind, p.Harness, p.DisplayName, p.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return s.putLaunchMetadata(ctx, p)
}

// PlacementFor finds a placement by the control-plane key or the worker engine id.
func (s *Store) PlacementFor(ctx context.Context, id string) (SessionPlacement, bool, error) {
	found, ok, err := s.Placement(ctx, id)
	if err != nil || ok {
		return found, ok, err
	}
	var p SessionPlacement
	var state, updated string
	err = s.db.QueryRowContext(ctx, `
SELECT session_id, node_id, state, detail, engine_id, project_id, kind, harness, display_name, updated_at
FROM fleet_session_placements WHERE engine_id = ?`, id).
		Scan(&p.SessionID, &p.NodeID, &state, &p.Detail, &p.EngineID, &p.ProjectID, &p.Kind, &p.Harness, &p.DisplayName, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionPlacement{}, false, nil
	}
	if err != nil {
		return SessionPlacement{}, false, err
	}
	p.State = Placement(state)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if err := s.hydratePlacement(ctx, &p); err != nil {
		return SessionPlacement{}, false, err
	}
	return p, true, nil
}

// Placement finds the durable record for a control-plane session key.
func (s *Store) Placement(ctx context.Context, sessionID string) (SessionPlacement, bool, error) {
	var p SessionPlacement
	var state, updated string
	err := s.db.QueryRowContext(ctx, `
SELECT session_id, node_id, state, detail, engine_id, project_id, kind, harness, display_name, updated_at
FROM fleet_session_placements WHERE session_id = ?`, sessionID).
		Scan(&p.SessionID, &p.NodeID, &state, &p.Detail, &p.EngineID, &p.ProjectID, &p.Kind, &p.Harness, &p.DisplayName, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionPlacement{}, false, nil
	}
	if err != nil {
		return SessionPlacement{}, false, err
	}
	p.State = Placement(state)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if err := s.hydratePlacement(ctx, &p); err != nil {
		return SessionPlacement{}, false, err
	}
	return p, true, nil
}

// ListPlacements returns durable ownership records and launch metadata.
func (s *Store) ListPlacements(ctx context.Context) ([]SessionPlacement, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT session_id, node_id, state, detail, engine_id, project_id, kind, harness, display_name, updated_at
FROM fleet_session_placements ORDER BY updated_at, session_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SessionPlacement
	for rows.Next() {
		var p SessionPlacement
		var state, updated string
		if err := rows.Scan(&p.SessionID, &p.NodeID, &state, &p.Detail, &p.EngineID, &p.ProjectID, &p.Kind, &p.Harness, &p.DisplayName, &updated); err != nil {
			return nil, err
		}
		p.State = Placement(state)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.hydratePlacement(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CountActive includes disconnected sessions until terminal intent is known.
func (s *Store) CountActive(ctx context.Context, nodeID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fleet_session_placements
WHERE node_id = ? AND state IN ('assigned', 'waking', 'starting', 'running', 'offline')`, nodeID).Scan(&n)
	return n, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNode(row scanner) (Node, error) {
	var n Node
	var role, availability, platforms, presence, lastSeen string
	var broker int
	err := row.Scan(&n.ID, &n.Name, &role, &availability, &n.Capacity, &platforms, &broker,
		&n.PowerBrokerNode, &n.WakeCommand, &n.ShutdownCommand, &n.ExpectedIdentity,
		&n.TokenHash, &presence, &lastSeen)
	if err != nil {
		return Node{}, err
	}
	n.Role = Role(role)
	n.Availability = Availability(availability)
	n.Presence = Presence(presence)
	n.PowerBroker = broker == 1
	if lastSeen != "" {
		n.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
	}
	if strings.TrimSpace(platforms) != "" {
		_ = json.Unmarshal([]byte(platforms), &n.Platforms)
	}
	return n, nil
}

func (s *Store) capabilities(ctx context.Context) (map[string][]Capability, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.node_id,c.harness,c.logged_in,c.version,c.reported_at,COALESCE(d.payload,'') FROM fleet_node_capabilities c LEFT JOIN fleet_capability_details d ON d.node_id=c.node_id AND d.harness=c.harness ORDER BY c.node_id,c.harness`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]Capability{}
	for rows.Next() {
		var nodeID, reported, detail string
		var capability Capability
		var logged int
		if err := rows.Scan(&nodeID, &capability.Harness, &logged, &capability.Version, &reported, &detail); err != nil {
			return nil, err
		}
		if detail != "" {
			if err := json.Unmarshal([]byte(detail), &capability); err != nil {
				return nil, err
			}
		}
		capability.LoggedIn = logged == 1
		capability.ReportedAt, _ = time.Parse(time.RFC3339Nano, reported)
		out[nodeID] = append(out[nodeID], capability)
	}
	return out, rows.Err()
}

func newToken() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(raw[:])
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
