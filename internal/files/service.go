package files

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"filemgr/internal/db"

	"github.com/google/uuid"
)

type Service struct {
	db *db.DB
}

func NewService(db *db.DB) *Service {
	return &Service{db: db}
}

func (s *Service) GetNode(ctx context.Context, id string) (*Node, error) {
	query := `
		SELECT n.id, n.parent_id, n.kind, n.name, n.name_key, n.created_at, n.updated_at, 
		       n.trashed_at, n.restore_parent_id, n.is_system,
		       fo.node_id, fo.storage_key, fo.size_bytes, fo.mime_sniffed, fo.sha256, fo.backup_selected_at, fo.created_at, fo.updated_at
		FROM nodes n
		LEFT JOIN file_objects fo ON n.id = fo.node_id
		WHERE n.id = ?;
	`
	row := s.db.QueryRowContext(ctx, query, id)
	return scanNode(row)
}

func (s *Service) CreateFolder(ctx context.Context, parentID *string, name string) (*Node, error) {
	dispName, nameKey, err := NormalizeName(name)
	if err != nil {
		return nil, err
	}

	targetParent := RootNodeID
	if parentID != nil && *parentID != "" {
		targetParent = *parentID
	}

	// Verify parent exists and is a folder and not trashed
	parent, err := s.GetNode(ctx, targetParent)
	if err != nil {
		return nil, err
	}
	if parent.Kind != KindFolder || parent.TrashedAt != nil {
		return nil, ErrInvalidOperation
	}

	nodeID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO nodes (id, parent_id, kind, name, name_key, created_at, updated_at, trashed_at, restore_parent_id, is_system)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0);
	`, nodeID, targetParent, KindFolder, dispName, nameKey, now, now)
	if err != nil {
		if isConstraintError(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("failed to insert folder: %w", err)
	}

	return s.GetNode(ctx, nodeID)
}

type CursorPayload struct {
	NameKey string `json:"k"`
	ID      string `json:"i"`
}

func (s *Service) ListNodes(ctx context.Context, parentID *string, cursor string, limit int, search string) (*ListNodesResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var cur CursorPayload
	if cursor != "" {
		data, err := base64.URLEncoding.DecodeString(cursor)
		if err == nil {
			_ = json.Unmarshal(data, &cur)
		}
	}

	var rows *sql.Rows
	var err error

	if search != "" {
		// Root-scoped active search across entire hierarchy
		_, searchKey, errNorm := NormalizeName(search)
		if errNorm != nil {
			searchKey = search
		}
		searchPattern := "%" + searchKey + "%"

		query := `
			SELECT n.id, n.parent_id, n.kind, n.name, n.name_key, n.created_at, n.updated_at, 
			       n.trashed_at, n.restore_parent_id, n.is_system,
			       fo.node_id, fo.storage_key, fo.size_bytes, fo.mime_sniffed, fo.sha256, fo.backup_selected_at, fo.created_at, fo.updated_at
			FROM nodes n
			LEFT JOIN file_objects fo ON n.id = fo.node_id
			WHERE n.trashed_at IS NULL AND n.is_system = 0 AND n.name_key LIKE ?
			  AND (n.name_key > ? OR (n.name_key = ? AND n.id > ?))
			ORDER BY n.name_key ASC, n.id ASC
			LIMIT ?;
		`
		rows, err = s.db.QueryContext(ctx, query, searchPattern, cur.NameKey, cur.NameKey, cur.ID, limit+1)
	} else {
		targetParent := RootNodeID
		if parentID != nil && *parentID != "" {
			targetParent = *parentID
		}

		query := `
			SELECT n.id, n.parent_id, n.kind, n.name, n.name_key, n.created_at, n.updated_at, 
			       n.trashed_at, n.restore_parent_id, n.is_system,
			       fo.node_id, fo.storage_key, fo.size_bytes, fo.mime_sniffed, fo.sha256, fo.backup_selected_at, fo.created_at, fo.updated_at
			FROM nodes n
			LEFT JOIN file_objects fo ON n.id = fo.node_id
			WHERE n.parent_id = ? AND n.trashed_at IS NULL AND n.is_system = 0
			  AND (n.kind < ? OR (n.kind = ? AND (n.name_key > ? OR (n.name_key = ? AND n.id > ?))))
			ORDER BY n.kind ASC, n.name_key ASC, n.id ASC
			LIMIT ?;
		`
		// Folder before file: 'folder' is > 'file' alphabetically, so we can order folders first
		// KindFolder = "folder", KindFile = "file" -> file is alphabetically before folder.
		// Let's explicitly order by CASE WHEN n.kind = 'folder' THEN 0 ELSE 1 END
		query = `
			SELECT n.id, n.parent_id, n.kind, n.name, n.name_key, n.created_at, n.updated_at, 
			       n.trashed_at, n.restore_parent_id, n.is_system,
			       fo.node_id, fo.storage_key, fo.size_bytes, fo.mime_sniffed, fo.sha256, fo.backup_selected_at, fo.created_at, fo.updated_at
			FROM nodes n
			LEFT JOIN file_objects fo ON n.id = fo.node_id
			WHERE n.parent_id = ? AND n.trashed_at IS NULL AND n.is_system = 0
			  AND (n.name_key > ? OR (n.name_key = ? AND n.id > ?))
			ORDER BY CASE WHEN n.kind = 'folder' THEN 0 ELSE 1 END, n.name_key ASC, n.id ASC
			LIMIT ?;
		`
		rows, err = s.db.QueryContext(ctx, query, targetParent, cur.NameKey, cur.NameKey, cur.ID, limit+1)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		node, err := scanNodeRow(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *node)
	}

	var nextCursor *string
	if len(nodes) > limit {
		last := nodes[limit-1]
		nodes = nodes[:limit]

		p := CursorPayload{NameKey: last.NameKey, ID: last.ID}
		bytes, _ := json.Marshal(p)
		enc := base64.URLEncoding.EncodeToString(bytes)
		nextCursor = &enc
	}

	return &ListNodesResult{
		Items:      nodes,
		NextCursor: nextCursor,
	}, nil
}

func (s *Service) RenameNode(ctx context.Context, id string, newName string) (*Node, error) {
	node, err := s.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if node.IsSystem {
		return nil, ErrSystemNodeProtected
	}

	dispName, nameKey, err := NormalizeName(newName)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		UPDATE nodes 
		SET name = ?, name_key = ?, updated_at = ?
		WHERE id = ?;
	`, dispName, nameKey, now, id)
	if err != nil {
		if isConstraintError(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("failed to rename node: %w", err)
	}

	return s.GetNode(ctx, id)
}

func (s *Service) MoveNode(ctx context.Context, id string, newParentID string) (*Node, error) {
	node, err := s.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if node.IsSystem {
		return nil, ErrSystemNodeProtected
	}

	if id == newParentID {
		return nil, ErrCycleDetected
	}

	targetParent, err := s.GetNode(ctx, newParentID)
	if err != nil {
		return nil, err
	}
	if targetParent.Kind != KindFolder || targetParent.TrashedAt != nil {
		return nil, ErrInvalidOperation
	}

	// Cycle detection: walk up from targetParent to root
	currID := newParentID
	for {
		if currID == id {
			return nil, ErrCycleDetected
		}
		var pID *string
		err := s.db.QueryRowContext(ctx, "SELECT parent_id FROM nodes WHERE id = ?", currID).Scan(&pID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			return nil, fmt.Errorf("cycle check failed: %w", err)
		}
		if pID == nil || *pID == "" {
			break
		}
		currID = *pID
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		UPDATE nodes 
		SET parent_id = ?, updated_at = ?
		WHERE id = ?;
	`, newParentID, now, id)
	if err != nil {
		if isConstraintError(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("failed to move node: %w", err)
	}

	return s.GetNode(ctx, id)
}

func (s *Service) TrashNode(ctx context.Context, id string) error {
	node, err := s.GetNode(ctx, id)
	if err != nil {
		return err
	}
	if node.IsSystem {
		return ErrSystemNodeProtected
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Soft-delete node to TRASH_ROOT
	_, err = tx.ExecContext(ctx, `
		UPDATE nodes 
		SET trashed_at = ?, restore_parent_id = parent_id, parent_id = ?, updated_at = ?
		WHERE id = ?;
	`, now, TrashRootNodeID, now, id)
	if err != nil {
		return fmt.Errorf("failed to trash node: %w", err)
	}

	// Recursively set trashed_at on all descendants
	if node.Kind == KindFolder {
		query := `
			WITH RECURSIVE subnodes(id) AS (
				SELECT id FROM nodes WHERE parent_id = ?
				UNION ALL
				SELECT n.id FROM nodes n JOIN subnodes s ON n.parent_id = s.id
			)
			UPDATE nodes 
			SET trashed_at = ?
			WHERE id IN (SELECT id FROM subnodes);
		`
		if _, err := tx.ExecContext(ctx, query, id, now); err != nil {
			return fmt.Errorf("failed to cascade trashed_at: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Service) ListTrash(ctx context.Context) ([]Node, error) {
	query := `
		SELECT n.id, n.parent_id, n.kind, n.name, n.name_key, n.created_at, n.updated_at, 
		       n.trashed_at, n.restore_parent_id, n.is_system,
		       fo.node_id, fo.storage_key, fo.size_bytes, fo.mime_sniffed, fo.sha256, fo.backup_selected_at, fo.created_at, fo.updated_at
		FROM nodes n
		LEFT JOIN file_objects fo ON n.id = fo.node_id
		WHERE n.parent_id = ? AND n.trashed_at IS NOT NULL
		ORDER BY n.trashed_at DESC;
	`
	rows, err := s.db.QueryContext(ctx, query, TrashRootNodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to list trash: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		node, err := scanNodeRow(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *node)
	}
	return nodes, nil
}

func (s *Service) RestoreNode(ctx context.Context, id string) (*Node, error) {
	node, err := s.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if node.TrashedAt == nil {
		return nil, ErrInvalidOperation
	}

	targetParent := RootNodeID
	if node.RestoreParentID != nil && *node.RestoreParentID != "" {
		// Verify if restore_parent_id still exists and is not trashed
		p, err := s.GetNode(ctx, *node.RestoreParentID)
		if err == nil && p.TrashedAt == nil && p.Kind == KindFolder {
			targetParent = *node.RestoreParentID
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Restore node
	_, err = tx.ExecContext(ctx, `
		UPDATE nodes 
		SET parent_id = ?, trashed_at = NULL, restore_parent_id = NULL, updated_at = ?
		WHERE id = ?;
	`, targetParent, now, id)
	if err != nil {
		if isConstraintError(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("failed to restore node: %w", err)
	}

	// Recursively clear trashed_at on all descendants
	if node.Kind == KindFolder {
		query := `
			WITH RECURSIVE subnodes(id) AS (
				SELECT id FROM nodes WHERE parent_id = ?
				UNION ALL
				SELECT n.id FROM nodes n JOIN subnodes s ON n.parent_id = s.id
			)
			UPDATE nodes 
			SET trashed_at = NULL
			WHERE id IN (SELECT id FROM subnodes);
		`
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return nil, fmt.Errorf("failed to clear trashed_at on descendants: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetNode(ctx, id)
}

// PurgeNode permanently removes a trashed node and its descendants from DB, returning list of physical storage keys to delete
func (s *Service) PurgeNode(ctx context.Context, id string) ([]string, error) {
	node, err := s.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if node.IsSystem {
		return nil, ErrSystemNodeProtected
	}
	if node.TrashedAt == nil {
		return nil, ErrInvalidOperation
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var storageKeys []string

	// Find all storage keys for this node and descendants
	findKeysQuery := `
		WITH RECURSIVE subnodes(id) AS (
			SELECT ?
			UNION ALL
			SELECT n.id FROM nodes n JOIN subnodes s ON n.parent_id = s.id
		)
		SELECT fo.storage_key 
		FROM file_objects fo 
		JOIN subnodes sn ON fo.node_id = sn.id;
	`
	rows, err := tx.QueryContext(ctx, findKeysQuery, id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err == nil {
				storageKeys = append(storageKeys, key)
			}
		}
	}

	// Delete all subnodes and target node
	deleteQuery := `
		WITH RECURSIVE subnodes(id) AS (
			SELECT ?
			UNION ALL
			SELECT n.id FROM nodes n JOIN subnodes s ON n.parent_id = s.id
		)
		DELETE FROM nodes WHERE id IN (SELECT id FROM subnodes);
	`
	if _, err := tx.ExecContext(ctx, deleteQuery, id); err != nil {
		return nil, fmt.Errorf("failed to delete node subtree: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return storageKeys, nil
}

func (s *Service) ToggleBackupSelection(ctx context.Context, nodeID string, selected bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var backupVal *string
	if selected {
		backupVal = &now
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE file_objects 
		SET backup_selected_at = ?, updated_at = ?
		WHERE node_id = ?;
	`, backupVal, now, nodeID)
	if err != nil {
		return fmt.Errorf("failed to update backup selection: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// Internal row scanning helpers
type scanner interface {
	Scan(dest ...any) error
}

func scanNode(s scanner) (*Node, error) {
	var (
		id, kind, name, nameKey, createdAt, updatedAt string
		parentID, trashedAt, restoreParentID          sql.NullString
		isSystem                                      int
		foNodeID, foStorageKey, foMime, foSha256      sql.NullString
		foSize                                        sql.NullInt64
		foBackupSelectedAt, foCreated, foUpdated      sql.NullString
	)

	err := s.Scan(
		&id, &parentID, &kind, &name, &nameKey, &createdAt, &updatedAt,
		&trashedAt, &restoreParentID, &isSystem,
		&foNodeID, &foStorageKey, &foSize, &foMime, &foSha256, &foBackupSelectedAt, &foCreated, &foUpdated,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	node := &Node{
		ID:       id,
		Kind:     NodeKind(kind),
		Name:     name,
		NameKey:  nameKey,
		IsSystem: isSystem == 1,
	}
	if parentID.Valid {
		node.ParentID = &parentID.String
	}
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		node.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
		node.UpdatedAt = t
	}
	if trashedAt.Valid {
		if t, err := time.Parse(time.RFC3339, trashedAt.String); err == nil {
			node.TrashedAt = &t
		}
	}
	if restoreParentID.Valid {
		node.RestoreParentID = &restoreParentID.String
	}

	if foNodeID.Valid {
		fo := &FileObject{
			NodeID:      foNodeID.String,
			StorageKey:  foStorageKey.String,
			SizeBytes:   foSize.Int64,
			MimeSniffed: foMime.String,
		}
		if foSha256.Valid {
			fo.Sha256 = &foSha256.String
		}
		if foBackupSelectedAt.Valid {
			if t, err := time.Parse(time.RFC3339, foBackupSelectedAt.String); err == nil {
				fo.BackupSelectedAt = &t
			}
		}
		if t, err := time.Parse(time.RFC3339, foCreated.String); err == nil {
			fo.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339, foUpdated.String); err == nil {
			fo.UpdatedAt = t
		}
		node.FileObject = fo
	}

	return node, nil
}

func scanNodeRow(rows *sql.Rows) (*Node, error) {
	return scanNode(rows)
}

func isConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, "UNIQUE constraint failed", "constraint failed")
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
