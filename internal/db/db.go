package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"filemgr/migrations"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(dbPath string, busyTimeout time.Duration, synchronous string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create db directory %q: %w", dir, err)
	}

	timeoutMs := busyTimeout.Milliseconds()
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	if synchronous == "" {
		synchronous = "NORMAL"
	}

	// Normalize db path for SQLite URI
	cleanPath := filepath.ToSlash(dbPath)
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(%s)",
		url.PathEscape(cleanPath), timeoutMs, synchronous)

	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Bounded connection pool for SQLite
	sqldb.SetMaxOpenConns(4)
	sqldb.SetMaxIdleConns(4)
	sqldb.SetConnMaxLifetime(time.Hour)

	// Verify connection and verify pragmas
	conn, err := sqldb.Conn(context.Background())
	if err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("failed to acquire test connection: %w", err)
	}
	defer conn.Close()

	initPragmas := []string{
		"PRAGMA foreign_keys = ON;",
		fmt.Sprintf("PRAGMA busy_timeout = %d;", timeoutMs),
		"PRAGMA journal_mode = WAL;",
		fmt.Sprintf("PRAGMA synchronous = %s;", synchronous),
	}
	for _, pragma := range initPragmas {
		if _, err := conn.ExecContext(context.Background(), pragma); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("failed executing pragma %q: %w", pragma, err)
		}
	}

	database := &DB{DB: sqldb}

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("failed running migrations: %w", err)
	}

	return database, nil
}

func (db *DB) Migrate(ctx context.Context) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("failed to read migrations: %w", err)
	}

	type migrationFile struct {
		version int
		name    string
	}
	var migs []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		migs = append(migs, migrationFile{version: v, name: entry.Name()})
	}

	sort.Slice(migs, func(i, j int) bool {
		return migs[i].version < migs[j].version
	})

	for _, m := range migs {
		var exists int
		err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM schema_migrations WHERE version = ?", m.version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed checking migration %d: %w", m.version, err)
		}
		if exists > 0 {
			continue
		}

		content, err := migrations.FS.ReadFile(m.name)
		if err != nil {
			return fmt.Errorf("failed reading migration %s: %w", m.name, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed starting tx for migration %d: %w", m.version, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed executing migration %s: %w", m.name, err)
		}

		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)", m.version, now); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed recording migration %d: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed committing migration %d: %w", m.version, err)
		}
	}

	return nil
}
