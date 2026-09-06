-- 001_initial_schema.sql
-- SQLite STRICT schema for self-hosted file manager

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    parent_id TEXT REFERENCES nodes(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK(kind IN ('file', 'folder')),
    name TEXT NOT NULL,
    name_key TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    trashed_at TEXT,
    restore_parent_id TEXT,
    is_system INTEGER NOT NULL DEFAULT 0 CHECK(is_system IN (0, 1))
) STRICT;

-- Sibling uniqueness for active (non-trashed) nodes under the same parent
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_active_sibling 
ON nodes(parent_id, name_key) 
WHERE trashed_at IS NULL;

-- Sibling uniqueness for active root items (where parent_id is NULL)
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_active_root_sibling 
ON nodes(name_key) 
WHERE parent_id IS NULL AND trashed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_nodes_parent_active 
ON nodes(parent_id, trashed_at);

CREATE INDEX IF NOT EXISTS idx_nodes_search 
ON nodes(name_key) 
WHERE trashed_at IS NULL;

CREATE TABLE IF NOT EXISTS file_objects (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
    mime_sniffed TEXT NOT NULL,
    sha256 TEXT,
    backup_selected_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_file_objects_backup 
ON file_objects(backup_selected_at) 
WHERE backup_selected_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS uploads (
    id TEXT PRIMARY KEY,
    owner_subject TEXT NOT NULL,
    parent_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL,
    expected_size INTEGER NOT NULL CHECK(expected_size >= 0),
    received_bytes INTEGER NOT NULL DEFAULT 0 CHECK(received_bytes >= 0),
    reservation_bytes INTEGER NOT NULL DEFAULT 0 CHECK(reservation_bytes >= 0),
    staging_key TEXT NOT NULL UNIQUE,
    final_storage_key TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL CHECK(state IN ('created', 'receiving', 'verifying', 'finalizing', 'complete', 'failed', 'expired', 'terminated')),
    sha256 TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_uploads_state_expires 
ON uploads(state, expires_at);

CREATE TABLE IF NOT EXISTS shares (
    id TEXT PRIMARY KEY,
    target_node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    password_hash TEXT,
    expires_at TEXT,
    revoked_at TEXT,
    auth_version INTEGER NOT NULL DEFAULT 1,
    global_share_epoch INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_shares_token_hash 
ON shares(token_hash);

CREATE INDEX IF NOT EXISTS idx_shares_target 
ON shares(target_node_id);

CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    request_id TEXT NOT NULL,
    actor_subject_hash TEXT NOT NULL,
    action TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK(outcome IN ('success', 'denied', 'error')),
    normalized_ip TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX IF NOT EXISTS idx_audit_created 
ON audit_events(created_at DESC);

CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    job_type TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending', 'running', 'completed', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    payload_json TEXT NOT NULL DEFAULT '{}',
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- Insert permanent system nodes if not present
INSERT OR IGNORE INTO nodes (id, parent_id, kind, name, name_key, created_at, updated_at, trashed_at, restore_parent_id, is_system)
VALUES ('ROOT', NULL, 'folder', 'Root', 'root', datetime('now'), datetime('now'), NULL, NULL, 1);

INSERT OR IGNORE INTO nodes (id, parent_id, kind, name, name_key, created_at, updated_at, trashed_at, restore_parent_id, is_system)
VALUES ('TRASH_ROOT', NULL, 'folder', 'Trash', 'trash', datetime('now'), datetime('now'), NULL, NULL, 1);
