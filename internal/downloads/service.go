package downloads

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"filemgr/internal/config"
	"filemgr/internal/files"
	"filemgr/internal/storage"
)

var (
	ErrDownloadLimitReached = errors.New("download concurrency limit reached")
	ErrFileNotFound         = errors.New("file not found")
)

type Service struct {
	storage      *storage.Storage
	filesSvc     *files.Service
	adminSem     chan struct{}
	publicSem    chan struct{}
}

func NewService(store *storage.Storage, filesSvc *files.Service, cfg *config.Config) *Service {
	maxAdmin := cfg.MaxActiveDownloads
	if maxAdmin <= 0 {
		maxAdmin = 8
	}
	maxPublic := cfg.MaxPublicDownloads
	if maxPublic <= 0 {
		maxPublic = 4
	}

	return &Service{
		storage:   store,
		filesSvc:  filesSvc,
		adminSem:  make(chan struct{}, maxAdmin),
		publicSem: make(chan struct{}, maxPublic),
	}
}

func (s *Service) ServeDownload(w http.ResponseWriter, r *http.Request, node *files.Node, isPublic bool) error {
	if node.Kind != files.KindFile || node.FileObject == nil {
		return ErrFileNotFound
	}

	sem := s.adminSem
	if isPublic {
		sem = s.publicSem
	}

	// Acquire concurrency semaphore with request context cancellation support
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-r.Context().Done():
		return r.Context().Err()
	default:
		return ErrDownloadLimitReached
	}

	f, err := s.storage.OpenObject(node.FileObject.StorageKey)
	if err != nil {
		return fmt.Errorf("failed to open object: %w", err)
	}
	defer f.Close()

	// Safe attachment filename header (RFC 6266)
	encodedName := url.PathEscape(node.Name)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", node.Name, encodedName))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if node.FileObject.MimeSniffed != "" {
		w.Header().Set("Content-Type", node.FileObject.MimeSniffed)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	// Strong ETag from stored whole-file SHA-256
	if node.FileObject.Sha256 != nil && *node.FileObject.Sha256 != "" {
		w.Header().Set("ETag", fmt.Sprintf("%q", *node.FileObject.Sha256))
	}

	if isPublic {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}

	// http.ServeContent handles Range, 206 Partial Content, If-Range, Content-Length, HEAD requests automatically
	http.ServeContent(w, r, node.Name, node.UpdatedAt, f)
	return nil
}
