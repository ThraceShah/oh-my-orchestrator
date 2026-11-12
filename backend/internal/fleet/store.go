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
    updated_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	// Tables created before engine_id existed keep working. A duplicate-column
	// error means this database already has the column.
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE fleet_session_placements ADD COLUMN engine_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	return nil
}

// Enrollment is the one-time result of adding a node. Token is shown once.
type Enrollment struct {
	Node  Node
	Token string
}

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

func (s *Store) List(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, role, availability, capacity, platforms, power_broker,
       power_broker_node_id, wake_command, shutdown_command, expected_identity,
       token_hash, presence, last_seen_at
FROM fleet_nodes ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	for _, cap := range caps {
		logged := 0
		if cap.LoggedIn {
			logged = 1
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO fleet_node_capabilities (node_id, harness, logged_in, version, reported_at)
VALUES (?, ?, ?, ?, ?)`, nodeID, cap.Harness, logged, cap.Version, now); err != nil {
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

func (s *Store) PutPlacement(ctx context.Context, p SessionPlacement) error {
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = s.now()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO fleet_session_placements (session_id, node_id, state, detail, engine_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    node_id = excluded.node_id,
    state = excluded.state,
    detail = excluded.detail,
    engine_id = CASE WHEN excluded.engine_id = '' THEN fleet_session_placements.engine_id ELSE excluded.engine_id END,
    updated_at = excluded.updated_at`,
		p.SessionID, p.NodeID, string(p.State), p.Detail, p.EngineID, p.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
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
SELECT session_id, node_id, state, detail, engine_id, updated_at
FROM fleet_session_placements WHERE engine_id = ?`, id).
		Scan(&p.SessionID, &p.NodeID, &state, &p.Detail, &p.EngineID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionPlacement{}, false, nil
	}
	if err != nil {
		return SessionPlacement{}, false, err
	}
	p.State = Placement(state)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return p, true, nil
}

func (s *Store) Placement(ctx context.Context, sessionID string) (SessionPlacement, bool, error) {
	var p SessionPlacement
	var state, updated string
	err := s.db.QueryRowContext(ctx, `
SELECT session_id, node_id, state, detail, engine_id, updated_at FROM fleet_session_placements WHERE session_id = ?`, sessionID).
		Scan(&p.SessionID, &p.NodeID, &state, &p.Detail, &p.EngineID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionPlacement{}, false, nil
	}
	if err != nil {
		return SessionPlacement{}, false, err
	}
	p.State = Placement(state)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return p, true, nil
}

func (s *Store) CountActive(ctx context.Context, nodeID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fleet_session_placements
WHERE node_id = ? AND state IN ('assigned', 'waking', 'starting', 'running')`, nodeID).Scan(&n)
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
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, harness, logged_in, version, reported_at FROM fleet_node_capabilities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Capability{}
	for rows.Next() {
		var nodeID, reported string
		var cap Capability
		var logged int
		if err := rows.Scan(&nodeID, &cap.Harness, &logged, &cap.Version, &reported); err != nil {
			return nil, err
		}
		cap.LoggedIn = logged == 1
		cap.ReportedAt, _ = time.Parse(time.RFC3339Nano, reported)
		out[nodeID] = append(out[nodeID], cap)
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
