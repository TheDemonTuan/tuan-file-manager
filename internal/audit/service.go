package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func (s *Service) Record(ctx context.Context, reqID, actor, action, objType, objID, outcome, ip string, metadata map[string]any) {
	actorHash := ""
	if actor != "" {
		hasher := sha256.New()
		hasher.Write([]byte(actor))
		actorHash = hex.EncodeToString(hasher.Sum(nil))
	}

	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		metaBytes = []byte("{}")
	}

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	// Insert audit event asynchronously or within context
	go func() {
		_, _ = s.db.ExecContext(context.Background(), `
			INSERT INTO audit_events (
				id, created_at, request_id, actor_subject_hash, action,
				object_type, object_id, outcome, normalized_ip, metadata_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
		`, id, now, reqID, actorHash, action, objType, objID, outcome, ip, string(metaBytes))
	}()
}
