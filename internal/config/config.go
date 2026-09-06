package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv               string
	AppListenAddr        string
	AppAdminHost         string
	AppShareHost         string
	DataDir              string
	DBPath               string
	DBBusyTimeout        time.Duration
	SQLiteSynchronous    string
	MaxStorageBytes      int64
	MaxFileBytes         int64
	MinFreeBytes         int64
	TrashRetentionDays   int
	TusChunkSize         int64
	MaxActiveUploads     int
	MaxActiveDownloads   int
	MaxPublicDownloads   int
	UploadExpiry         time.Duration
	CFAccessTeamDomain   string
	CFAccessAud          string
	CFAccessAllowedEmail string
	DevAuthBypass        bool
	DevAdminEmail        string
	TrustedProxyMode     string
	ShareTokenPepper     string
	ShareSessionSecret   string
	GlobalShareEpoch     int64
	BackupEnabled        bool
	BackupSnapshotTTL    time.Duration
}

func Load() (*Config, error) {
	dataDir := getEnv("DATA_DIR", "./data")
	dbPath := getEnv("DB_PATH", filepath.Join(dataDir, "db", "app.db"))

	cfg := &Config{
		AppEnv:               getEnv("APP_ENV", "development"),
		AppListenAddr:        getEnv("APP_LISTEN_ADDR", ":8080"),
		AppAdminHost:         getEnv("APP_ADMIN_HOST", "localhost:8080"),
		AppShareHost:         getEnv("APP_SHARE_HOST", "share.localhost:8080"),
		DataDir:              dataDir,
		DBPath:               dbPath,
		DBBusyTimeout:        time.Duration(getEnvInt("DB_BUSY_TIMEOUT_MS", 5000)) * time.Millisecond,
		SQLiteSynchronous:    getEnv("SQLITE_SYNCHRONOUS", "NORMAL"),
		MaxStorageBytes:      getEnvInt64("MAX_STORAGE_BYTES", 500*1024*1024*1024), // 500 GiB
		MaxFileBytes:         getEnvInt64("MAX_FILE_BYTES", 10*1024*1024*1024),     // 10 GiB
		MinFreeBytes:         getEnvInt64("MIN_FREE_BYTES", 5*1024*1024*1024),      // 5 GiB
		TrashRetentionDays:   getEnvInt("TRASH_RETENTION_DAYS", 30),
		TusChunkSize:         getEnvInt64("TUS_CHUNK_SIZE", 16*1024*1024), // 16 MiB
		MaxActiveUploads:     getEnvInt("MAX_ACTIVE_UPLOADS", 4),
		MaxActiveDownloads:   getEnvInt("MAX_ACTIVE_DOWNLOADS", 8),
		MaxPublicDownloads:   getEnvInt("MAX_PUBLIC_DOWNLOADS", 4),
		UploadExpiry:         time.Duration(getEnvInt("UPLOAD_EXPIRY_HOURS", 24)) * time.Hour,
		CFAccessTeamDomain:   getEnv("CF_ACCESS_TEAM_DOMAIN", ""),
		CFAccessAud:          getEnv("CF_ACCESS_AUD", ""),
		CFAccessAllowedEmail: getEnv("CF_ACCESS_ALLOWED_SUBJECT", ""),
		DevAuthBypass:        getEnvBool("DEV_AUTH_BYPASS", true), // default true for convenience if unset in dev
		DevAdminEmail:        getEnv("DEV_ADMIN_EMAIL", "admin@example.com"),
		TrustedProxyMode:     getEnv("TRUSTED_PROXY_MODE", "cloudflare"),
		ShareTokenPepper:     getEnv("SHARE_TOKEN_PEPPER", "insecure-default-pepper-change-me-in-prod"),
		ShareSessionSecret:   getEnv("SHARE_SESSION_SECRET", "insecure-default-secret-change-me-in-prod"),
		GlobalShareEpoch:     getEnvInt64("GLOBAL_SHARE_EPOCH", 1),
		BackupEnabled:        getEnvBool("BACKUP_ENABLED", false),
		BackupSnapshotTTL:    time.Duration(getEnvInt("BACKUP_SNAPSHOT_TTL_HOURS", 6)) * time.Hour,
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.MaxFileBytes <= 0 {
		return fmt.Errorf("MAX_FILE_BYTES must be positive")
	}
	if c.TusChunkSize <= 0 || c.TusChunkSize > 90*1024*1024 {
		return fmt.Errorf("TUS_CHUNK_SIZE must be between 1 byte and 90 MiB (Cloudflare 100MB limit)")
	}
	if c.AppEnv == "production" {
		if c.DevAuthBypass {
			return fmt.Errorf("DEV_AUTH_BYPASS must be false in production")
		}
		if c.CFAccessAud == "" {
			return fmt.Errorf("CF_ACCESS_AUD is required in production")
		}
		if c.CFAccessTeamDomain == "" {
			return fmt.Errorf("CF_ACCESS_TEAM_DOMAIN is required in production")
		}
		if strings.Contains(c.ShareTokenPepper, "insecure-default") {
			return fmt.Errorf("SHARE_TOKEN_PEPPER must be set to a secure secret in production")
		}
		if strings.Contains(c.ShareSessionSecret, "insecure-default") {
			return fmt.Errorf("SHARE_SESSION_SECRET must be set to a secure secret in production")
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return strings.TrimSpace(val)
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return i
}

func getEnvInt64(key string, fallback int64) int64 {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	i, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return fallback
	}
	return i
}

func getEnvBool(key string, fallback bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return fallback
	}
	return b
}
