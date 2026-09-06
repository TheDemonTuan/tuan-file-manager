package shares

import (
	"errors"
	"time"

	"filemgr/internal/files"
)

var (
	ErrShareNotFound      = errors.New("share not found")
	ErrShareRevoked       = errors.New("share has been revoked")
	ErrShareExpired       = errors.New("share has expired")
	ErrPasswordRequired   = errors.New("share requires password")
	ErrInvalidPassword    = errors.New("invalid share password")
	ErrInvalidSession     = errors.New("invalid or expired share session")
	ErrAccessDenied       = errors.New("requested item is outside the shared folder scope")
	ErrTargetNodeNotFound = errors.New("shared target item does not exist")
)

type Share struct {
	ID               string      `json:"id"`
	TargetNodeID     string      `json:"target_node_id"`
	TokenHash        string      `json:"-"`
	PasswordHash     *string     `json:"-"`
	HasPassword      bool        `json:"has_password"`
	ExpiresAt        *time.Time  `json:"expires_at,omitempty"`
	RevokedAt        *time.Time  `json:"revoked_at,omitempty"`
	AuthVersion      int         `json:"auth_version"`
	GlobalShareEpoch int64       `json:"global_share_epoch"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	TargetNode       *files.Node `json:"target_node,omitempty"`
}

type CreateShareResult struct {
	Share *Share `json:"share"`
	Token string `json:"token"` // Plaintext token returned exactly once
}

type PublicShareView struct {
	ID           string      `json:"id"`
	TargetNodeID string      `json:"target_node_id"`
	TargetName   string      `json:"target_name"`
	TargetKind   string      `json:"target_kind"`
	SizeBytes    int64       `json:"size_bytes"`
	NeedPassword bool        `json:"need_password"`
	Unlocked     bool        `json:"unlocked"`
	TargetNode   *files.Node `json:"target_node,omitempty"`
}
