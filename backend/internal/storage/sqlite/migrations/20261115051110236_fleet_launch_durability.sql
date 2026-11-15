-- +goose Up
-- Fleet base tables are initialized only by a control-plane daemon. These
-- additive side tables also exist on workers so migrations have no role order.
CREATE TABLE IF NOT EXISTS fleet_launch_requests (
    session_id TEXT PRIMARY KEY,
    parent_id TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT '',
    request_hash TEXT NOT NULL DEFAULT '',
    payload TEXT NOT NULL DEFAULT '',
    dispatch_state TEXT NOT NULL DEFAULT 'pending'
);
CREATE TABLE IF NOT EXISTS fleet_capability_details (
    node_id TEXT NOT NULL,
    harness TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (node_id, harness)
);
CREATE TABLE IF NOT EXISTS fleet_supervision_inbox (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    payload TEXT NOT NULL,
    received_at TEXT NOT NULL,
    acknowledged_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS fleet_supervision_inbox;
DROP TABLE IF EXISTS fleet_capability_details;
DROP TABLE IF EXISTS fleet_launch_requests;
