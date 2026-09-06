package uploads

import (
	"errors"
	"time"
)

var (
	ErrUploadNotFound       = errors.New("upload session not found")
	ErrInvalidState         = errors.New("invalid upload state for operation")
	ErrOffsetMismatch       = errors.New("upload offset mismatch")
	ErrLengthMismatch       = errors.New("received bytes exceeded expected size")
	ErrUploadExpired        = errors.New("upload session expired")
	ErrQuotaExceeded        = errors.New("storage quota exceeded")
	ErrPhysicalSpaceBlocked = errors.New("physical free storage space threshold crossed")
	ErrFileTooLarge         = errors.New("file exceeds maximum allowed size")
)

type UploadState string

const (
	StateCreated    UploadState = "created"
	StateReceiving  UploadState = "receiving"
	StateVerifying  UploadState = "verifying"
	StateFinalizing UploadState = "finalizing"
	StateComplete   UploadState = "complete"
	StateFailed     UploadState = "failed"
	StateExpired    UploadState = "expired"
	StateTerminated UploadState = "terminated"
)

type Upload struct {
	ID               string      `json:"id"`
	OwnerSubject     string      `json:"owner_subject"`
	ParentID         string      `json:"parent_id"`
	Name             string      `json:"name"`
	NameKey          string      `json:"name_key"`
	ExpectedSize     int64       `json:"expected_size"`
	ReceivedBytes    int64       `json:"received_bytes"`
	ReservationBytes int64       `json:"reservation_bytes"`
	StagingKey       string      `json:"staging_key"`
	FinalStorageKey  string      `json:"final_storage_key"`
	State            UploadState `json:"state"`
	Sha256           *string     `json:"sha256,omitempty"`
	LastError        *string     `json:"last_error,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	ExpiresAt        time.Time   `json:"expires_at"`
}
