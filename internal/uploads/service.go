package uploads

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"filemgr/internal/config"
	"filemgr/internal/db"
	"filemgr/internal/files"
	"filemgr/internal/storage"

	"github.com/google/uuid"
)

type Service struct {
	db       *db.DB
	storage  *storage.Storage
	filesSvc *files.Service
	cfg      *config.Config
	mu       sync.Mutex
}

func NewService(db *db.DB, storage *storage.Storage, filesSvc *files.Service, cfg *config.Config) *Service {
	return &Service{
		db:       db,
		storage:  storage,
		filesSvc: filesSvc,
		cfg:      cfg,
	}
}

func (s *Service) CreateUpload(ctx context.Context, ownerSubject, parentID, filename string, expectedSize int64) (*Upload, error) {
	if expectedSize < 0 {
		return nil, errors.New("expected_size must be non-negative")
	}
	if expectedSize > s.cfg.MaxFileBytes {
		return nil, ErrFileTooLarge
	}

	dispName, nameKey, err := files.NormalizeName(filename)
	if err != nil {
		return nil, err
	}

	targetParent := files.RootNodeID
	if parentID != "" {
		targetParent = parentID
	}

	// Verify target parent folder exists and is active
	parent, err := s.filesSvc.GetNode(ctx, targetParent)
	if err != nil {
		return nil, err
	}
	if parent.Kind != files.KindFolder || parent.TrashedAt != nil {
		return nil, files.ErrInvalidOperation
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check sibling conflict in DB
	var conflictCount int
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM nodes 
		WHERE parent_id = ? AND name_key = ? AND trashed_at IS NULL;
	`, targetParent, nameKey).Scan(&conflictCount)
	if err != nil {
		return nil, fmt.Errorf("failed to check sibling conflict: %w", err)
	}
	if conflictCount > 0 {
		return nil, files.ErrAlreadyExists
	}

	// Check storage quota (file_objects + active reservations + expectedSize <= MaxStorageBytes)
	var storedBytes, reservedBytes int64
	_ = s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(size_bytes), 0) FROM file_objects;").Scan(&storedBytes)
	_ = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(reservation_bytes), 0) FROM uploads 
		WHERE state IN ('created', 'receiving', 'verifying', 'finalizing');
	`).Scan(&reservedBytes)

	if storedBytes+reservedBytes+expectedSize > s.cfg.MaxStorageBytes {
		return nil, ErrQuotaExceeded
	}

	disk, err := storage.GetDiskSpace(s.storage.BaseDir())
	if err == nil && disk.FreeBytes > 0 {
		if int64(disk.FreeBytes)-expectedSize < s.cfg.MinFreeBytes {
			return nil, ErrPhysicalSpaceBlocked
		}
	}

	uploadID := uuid.New().String()
	stagingKey := s.storage.GenerateOpaqueKey()
	finalStorageKey := s.storage.GenerateOpaqueKey()
	now := time.Now().UTC()
	expiresAt := now.Add(s.cfg.UploadExpiry)

	// Create staging file
	stagingFile, err := s.storage.CreateStagingFile(stagingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create staging file: %w", err)
	}
	_ = stagingFile.Close()

	nowStr := now.Format(time.RFC3339)
	expStr := expiresAt.Format(time.RFC3339)

	query := `
		INSERT INTO uploads (
			id, owner_subject, parent_id, name, name_key, expected_size,
			received_bytes, reservation_bytes, staging_key, final_storage_key,
			state, sha256, last_error, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, NULL, NULL, ?, ?, ?);
	`
	_, err = s.db.ExecContext(ctx, query,
		uploadID, ownerSubject, targetParent, dispName, nameKey, expectedSize,
		expectedSize, stagingKey, finalStorageKey, string(StateCreated),
		nowStr, nowStr, expStr,
	)
	if err != nil {
		_ = s.storage.RemoveStagingFile(stagingKey)
		return nil, fmt.Errorf("failed to insert upload: %w", err)
	}

	return s.GetUpload(ctx, uploadID)
}

func (s *Service) GetUpload(ctx context.Context, id string) (*Upload, error) {
	query := `
		SELECT id, owner_subject, parent_id, name, name_key, expected_size,
		       received_bytes, reservation_bytes, staging_key, final_storage_key,
		       state, sha256, last_error, created_at, updated_at, expires_at
		FROM uploads WHERE id = ?;
	`
	var (
		u                                         Upload
		st                                        string
		sha, lastErr                              sql.NullString
		createdStr, updatedStr, expStr            string
	)
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&u.ID, &u.OwnerSubject, &u.ParentID, &u.Name, &u.NameKey, &u.ExpectedSize,
		&u.ReceivedBytes, &u.ReservationBytes, &u.StagingKey, &u.FinalStorageKey,
		&st, &sha, &lastErr, &createdStr, &updatedStr, &expStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUploadNotFound
		}
		return nil, err
	}
	u.State = UploadState(st)
	if sha.Valid {
		u.Sha256 = &sha.String
	}
	if lastErr.Valid {
		u.LastError = &lastErr.String
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	u.ExpiresAt, _ = time.Parse(time.RFC3339, expStr)
	return &u, nil
}

func (s *Service) WriteChunk(ctx context.Context, id string, offset int64, reader io.Reader) (*Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, err := s.GetUpload(ctx, id)
	if err != nil {
		return nil, err
	}

	if u.State != StateCreated && u.State != StateReceiving {
		return nil, ErrInvalidState
	}
	if time.Now().UTC().After(u.ExpiresAt) {
		return nil, ErrUploadExpired
	}
	if offset != u.ReceivedBytes {
		return nil, ErrOffsetMismatch
	}

	stagingFile, err := s.storage.OpenStagingForAppend(u.StagingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to open staging file: %w", err)
	}

	// Write chunk
	n, err := io.Copy(stagingFile, reader)
	_ = stagingFile.Sync()
	_ = stagingFile.Close()

	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed writing to staging: %w", err)
	}

	newReceived := u.ReceivedBytes + n
	if newReceived > u.ExpectedSize {
		return nil, ErrLengthMismatch
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	newState := StateReceiving
	if newReceived == u.ExpectedSize {
		newState = StateVerifying
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE uploads 
		SET received_bytes = ?, state = ?, updated_at = ?
		WHERE id = ?;
	`, newReceived, string(newState), nowStr, id)
	if err != nil {
		return nil, fmt.Errorf("failed to update upload progress: %w", err)
	}

	u.ReceivedBytes = newReceived
	u.State = newState

	if newState == StateVerifying {
		if err := s.finalizeUpload(ctx, u); err != nil {
			return nil, fmt.Errorf("finalization failed: %w", err)
		}
		return s.GetUpload(ctx, id)
	}

	return u, nil
}

func (s *Service) finalizeUpload(ctx context.Context, u *Upload) error {
	stagingPath, err := s.storage.StagingPath(u.StagingKey)
	if err != nil {
		return err
	}

	// Calculate whole-file SHA-256
	f, err := os.Open(stagingPath)
	if err != nil {
		return fmt.Errorf("failed opening staging file for checksum: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		f.Close()
		return fmt.Errorf("failed computing sha256: %w", err)
	}
	f.Close()
	hashStr := hex.EncodeToString(hasher.Sum(nil))

	// Sniff MIME type
	mime, _ := s.storage.SniffMimeType(stagingPath)

	// Phase A — Durable intent
	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		UPDATE uploads 
		SET state = ?, sha256 = ?, updated_at = ?
		WHERE id = ?;
	`, string(StateFinalizing), hashStr, nowStr, u.ID)
	if err != nil {
		return fmt.Errorf("phase A durable intent failed: %w", err)
	}

	// Phase B — Filesystem commit (atomic rename)
	if err := s.storage.MoveStagingToObject(u.StagingKey, u.FinalStorageKey); err != nil {
		return fmt.Errorf("phase B filesystem commit failed: %w", err)
	}

	// Phase C — Metadata commit
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed starting phase C tx: %w", err)
	}
	defer tx.Rollback()

	newNodeID := uuid.New().String()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO nodes (id, parent_id, kind, name, name_key, created_at, updated_at, trashed_at, restore_parent_id, is_system)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0);
	`, newNodeID, u.ParentID, string(files.KindFile), u.Name, u.NameKey, nowStr, nowStr)
	if err != nil {
		return fmt.Errorf("phase C node insertion failed: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO file_objects (node_id, storage_key, size_bytes, mime_sniffed, sha256, backup_selected_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULL, ?, ?);
	`, newNodeID, u.FinalStorageKey, u.ExpectedSize, mime, hashStr, nowStr, nowStr)
	if err != nil {
		return fmt.Errorf("phase C file_object insertion failed: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE uploads 
		SET state = ?, reservation_bytes = 0, updated_at = ?
		WHERE id = ?;
	`, string(StateComplete), nowStr, u.ID)
	if err != nil {
		return fmt.Errorf("phase C upload completion failed: %w", err)
	}

	return tx.Commit()
}

func (s *Service) TerminateUpload(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, err := s.GetUpload(ctx, id)
	if err != nil {
		return err
	}

	if u.State == StateComplete {
		return ErrInvalidState
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		UPDATE uploads 
		SET state = ?, reservation_bytes = 0, updated_at = ?
		WHERE id = ?;
	`, string(StateTerminated), nowStr, id)
	if err != nil {
		return err
	}

	_ = s.storage.RemoveStagingFile(u.StagingKey)
	return nil
}

// Reconcile checks for interrupted finalizations or expired uploads
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	// 1. Expire stale uploads
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, staging_key FROM uploads 
		WHERE state IN ('created', 'receiving') AND expires_at < ?;
	`, nowStr)
	if err == nil {
		var expiredIDs []string
		var stagingKeys []string
		for rows.Next() {
			var id, key string
			if err := rows.Scan(&id, &key); err == nil {
				expiredIDs = append(expiredIDs, id)
				stagingKeys = append(stagingKeys, key)
			}
		}
		rows.Close()

		for i, id := range expiredIDs {
			_, _ = s.db.ExecContext(ctx, `
				UPDATE uploads 
				SET state = ?, reservation_bytes = 0, updated_at = ?
				WHERE id = ?;
			`, string(StateExpired), nowStr, id)
			_ = s.storage.RemoveStagingFile(stagingKeys[i])
		}
	}

	// 2. Resolve 'finalizing' uploads (crash recovery)
	fRows, err := s.db.QueryContext(ctx, `
		SELECT id, parent_id, name, name_key, expected_size, staging_key, final_storage_key, sha256
		FROM uploads WHERE state = ?;
	`, string(StateFinalizing))
	if err != nil {
		return err
	}
	defer fRows.Close()

	type finalizingUpload struct {
		id, parentID, name, nameKey, stagingKey, finalStorageKey string
		expectedSize                                             int64
		sha256                                                   sql.NullString
	}
	var finalizingList []finalizingUpload

	for fRows.Next() {
		var fu finalizingUpload
		if err := fRows.Scan(&fu.id, &fu.parentID, &fu.name, &fu.nameKey, &fu.expectedSize, &fu.stagingKey, &fu.finalStorageKey, &fu.sha256); err == nil {
			finalizingList = append(finalizingList, fu)
		}
	}
	fRows.Close()

	for _, fu := range finalizingList {
		finalExists := s.storage.ObjectExists(fu.finalStorageKey)
		stagingExists := s.storage.StagingExists(fu.stagingKey)

		// Case 1: Final object exists on disk
		if finalExists {
			// Check if file_object already committed
			var count int
			_ = s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM file_objects WHERE storage_key = ?", fu.finalStorageKey).Scan(&count)
			if count == 0 {
				// Finish Phase C
				newNodeID := uuid.New().String()
				mime := "application/octet-stream"
				hash := ""
				if fu.sha256.Valid {
					hash = fu.sha256.String
				}
				tx, err := s.db.BeginTx(ctx, nil)
				if err == nil {
					_, _ = tx.ExecContext(ctx, `
						INSERT INTO nodes (id, parent_id, kind, name, name_key, created_at, updated_at, trashed_at, restore_parent_id, is_system)
						VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0);
					`, newNodeID, fu.parentID, string(files.KindFile), fu.name, fu.nameKey, nowStr, nowStr)
					_, _ = tx.ExecContext(ctx, `
						INSERT INTO file_objects (node_id, storage_key, size_bytes, mime_sniffed, sha256, backup_selected_at, created_at, updated_at)
						VALUES (?, ?, ?, ?, ?, NULL, ?, ?);
					`, newNodeID, fu.finalStorageKey, fu.expectedSize, mime, hash, nowStr, nowStr)
					_, _ = tx.ExecContext(ctx, `
						UPDATE uploads 
						SET state = ?, reservation_bytes = 0, updated_at = ?
						WHERE id = ?;
					`, string(StateComplete), nowStr, fu.id)
					_ = tx.Commit()
				}
			} else {
				_, _ = s.db.ExecContext(ctx, `
					UPDATE uploads 
					SET state = ?, reservation_bytes = 0, updated_at = ?
					WHERE id = ?;
				`, string(StateComplete), nowStr, fu.id)
			}
			if stagingExists {
				_ = s.storage.RemoveStagingFile(fu.stagingKey)
			}
		} else if stagingExists {
			// Case 2: Staging exists, retry move
			if err := s.storage.MoveStagingToObject(fu.stagingKey, fu.finalStorageKey); err == nil {
				// Retry phase C
				newNodeID := uuid.New().String()
				hash := ""
				if fu.sha256.Valid {
					hash = fu.sha256.String
				}
				tx, err := s.db.BeginTx(ctx, nil)
				if err == nil {
					_, _ = tx.ExecContext(ctx, `
						INSERT INTO nodes (id, parent_id, kind, name, name_key, created_at, updated_at, trashed_at, restore_parent_id, is_system)
						VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0);
					`, newNodeID, fu.parentID, string(files.KindFile), fu.name, fu.nameKey, nowStr, nowStr)
					_, _ = tx.ExecContext(ctx, `
						INSERT INTO file_objects (node_id, storage_key, size_bytes, mime_sniffed, sha256, backup_selected_at, created_at, updated_at)
						VALUES (?, ?, ?, ?, ?, NULL, ?, ?);
					`, newNodeID, fu.finalStorageKey, fu.expectedSize, "application/octet-stream", hash, nowStr, nowStr)
					_, _ = tx.ExecContext(ctx, `
						UPDATE uploads 
						SET state = ?, reservation_bytes = 0, updated_at = ?
						WHERE id = ?;
					`, string(StateComplete), nowStr, fu.id)
					_ = tx.Commit()
				}
			}
		} else {
			// Case 4: Neither exists -> mark failed
			_, _ = s.db.ExecContext(ctx, `
				UPDATE uploads 
				SET state = ?, reservation_bytes = 0, updated_at = ?
				WHERE id = ?;
			`, string(StateFailed), nowStr, fu.id)
		}
	}

	return nil
}
