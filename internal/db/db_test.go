package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDBInitializationAndMigrations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "filemgr-db-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := Open(dbPath, 2*time.Second, "NORMAL")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// Verify journal_mode
	var journalMode string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode;").Scan(&journalMode); err != nil {
		t.Fatalf("failed to query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode wal, got %s", journalMode)
	}

	// Verify foreign_keys
	var foreignKeys int
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&foreignKeys); err != nil {
		t.Fatalf("failed to query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("expected foreign_keys 1, got %d", foreignKeys)
	}

	// Verify system nodes ROOT and TRASH_ROOT exist
	var rootCount int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM nodes WHERE id IN ('ROOT', 'TRASH_ROOT')").Scan(&rootCount); err != nil {
		t.Fatalf("failed to query system nodes: %v", err)
	}
	if rootCount != 2 {
		t.Errorf("expected 2 system nodes, got %d", rootCount)
	}
}
