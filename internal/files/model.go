package files

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrNotFound             = errors.New("node not found")
	ErrAlreadyExists        = errors.New("node with this name already exists in target folder")
	ErrInvalidName          = errors.New("invalid node name")
	ErrCycleDetected        = errors.New("cannot move folder into itself or a descendant")
	ErrSystemNodeProtected  = errors.New("system node cannot be modified or deleted")
	ErrInvalidOperation     = errors.New("invalid operation")
	ErrStorageQuotaExceeded = errors.New("storage quota exceeded")
	ErrInsufficientSpace    = errors.New("insufficient physical space")
)

const (
	RootNodeID      = "ROOT"
	TrashRootNodeID = "TRASH_ROOT"
)

type NodeKind string

const (
	KindFile   NodeKind = "file"
	KindFolder NodeKind = "folder"
)

type Node struct {
	ID              string      `json:"id"`
	ParentID        *string     `json:"parent_id"`
	Kind            NodeKind    `json:"kind"`
	Name            string      `json:"name"`
	NameKey         string      `json:"name_key"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	TrashedAt       *time.Time  `json:"trashed_at,omitempty"`
	RestoreParentID *string     `json:"restore_parent_id,omitempty"`
	IsSystem        bool        `json:"is_system"`
	FileObject      *FileObject `json:"file_object,omitempty"`
}

type FileObject struct {
	NodeID           string     `json:"node_id"`
	StorageKey       string     `json:"storage_key"`
	SizeBytes        int64      `json:"size_bytes"`
	MimeSniffed      string     `json:"mime_sniffed"`
	Sha256           *string    `json:"sha256,omitempty"`
	BackupSelectedAt *time.Time `json:"backup_selected_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type ListNodesResult struct {
	Items      []Node  `json:"items"`
	NextCursor *string `json:"next_cursor,omitempty"`
}

// NormalizeName generates a canonical NFC case-folded key for uniqueness comparisons
func NormalizeName(name string) (displayName string, nameKey string, err error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 255 {
		return "", "", ErrInvalidName
	}
	// Path traversal or invalid characters check
	if strings.ContainsAny(trimmed, "/\\\x00\r\n") || trimmed == "." || trimmed == ".." {
		return "", "", ErrInvalidName
	}
	nfc := norm.NFC.String(trimmed)
	key := strings.ToLower(nfc)
	return nfc, key, nil
}
