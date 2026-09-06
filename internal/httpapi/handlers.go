package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"filemgr/internal/audit"
	"filemgr/internal/auth"
	"filemgr/internal/config"
	"filemgr/internal/db"
	"filemgr/internal/downloads"
	"filemgr/internal/files"
	"filemgr/internal/observability"
	"filemgr/internal/shares"
	"filemgr/internal/storage"
	"filemgr/internal/uploads"
)

type API struct {
	cfg          *config.Config
	db           *db.DB
	filesSvc     *files.Service
	uploadsSvc   *uploads.Service
	downloadsSvc *downloads.Service
	sharesSvc    *shares.Service
	auditSvc     *audit.Service
	authVerifier *auth.Verifier
	store        *storage.Storage
}

func NewAPI(
	cfg *config.Config,
	database *db.DB,
	filesSvc *files.Service,
	uploadsSvc *uploads.Service,
	downloadsSvc *downloads.Service,
	sharesSvc *shares.Service,
	auditSvc *audit.Service,
	authVerifier *auth.Verifier,
	store *storage.Storage,
) *API {
	return &API{
		cfg:          cfg,
		db:           database,
		filesSvc:     filesSvc,
		uploadsSvc:   uploadsSvc,
		downloadsSvc: downloadsSvc,
		sharesSvc:    sharesSvc,
		auditSvc:     auditSvc,
		authVerifier: authVerifier,
		store:        store,
	}
}

func (a *API) getContextActor(r *http.Request) (string, string) {
	reqID, _ := r.Context().Value(observability.RequestIDKey).(string)
	identity, _ := r.Context().Value(observability.IdentityKey).(string)
	if identity == "" {
		identity = "anonymous"
	}
	return reqID, identity
}

func (a *API) clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// Health checks
func (a *API) HandleHealthLive(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) HandleHealthReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := a.db.PingContext(ctx); err != nil {
		WriteProblem(w, r, http.StatusServiceUnavailable, "Database Unavailable", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Admin /api/v1/me
func (a *API) HandleGetMe(w http.ResponseWriter, r *http.Request) {
	_, identity := a.getContextActor(r)
	WriteJSON(w, http.StatusOK, map[string]string{"identity": identity})
}

// Admin /api/v1/nodes
func (a *API) HandleListNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	parentID := q.Get("parent_id")
	cursor := q.Get("cursor")
	search := q.Get("search")
	limit, _ := strconv.Atoi(q.Get("limit"))

	var pID *string
	if parentID != "" {
		pID = &parentID
	}

	res, err := a.filesSvc.ListNodes(r.Context(), pID, cursor, limit, search)
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, "Failed to list nodes", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// Admin POST /api/v1/folders
func (a *API) HandleCreateFolder(w http.ResponseWriter, r *http.Request) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	var body struct {
		ParentID *string `json:"parent_id"`
		Name     string  `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}

	node, err := a.filesSvc.CreateFolder(r.Context(), body.ParentID, body.Name)
	if err != nil {
		outcome := "error"
		if errors.Is(err, files.ErrAlreadyExists) {
			outcome = "denied"
			WriteProblem(w, r, http.StatusConflict, "Name Conflict", err.Error())
		} else if errors.Is(err, files.ErrInvalidName) || errors.Is(err, files.ErrInvalidOperation) {
			outcome = "denied"
			WriteProblem(w, r, http.StatusBadRequest, "Invalid Folder", err.Error())
		} else {
			WriteProblem(w, r, http.StatusInternalServerError, "Server Error", err.Error())
		}
		a.auditSvc.Record(r.Context(), reqID, actor, "node.create_folder", "folder", "", outcome, ip, map[string]any{"name": body.Name})
		return
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "node.create_folder", "folder", node.ID, "success", ip, map[string]any{"name": node.Name})
	WriteJSON(w, http.StatusCreated, node)
}

// Admin PATCH /api/v1/nodes/{id}
func (a *API) HandleRenameNode(w http.ResponseWriter, r *http.Request, id string) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}

	node, err := a.filesSvc.RenameNode(r.Context(), id, body.Name)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) {
			WriteProblem(w, r, http.StatusNotFound, "Not Found", err.Error())
		} else if errors.Is(err, files.ErrAlreadyExists) {
			WriteProblem(w, r, http.StatusConflict, "Name Conflict", err.Error())
		} else if errors.Is(err, files.ErrSystemNodeProtected) {
			WriteProblem(w, r, http.StatusForbidden, "Protected System Node", err.Error())
		} else {
			WriteProblem(w, r, http.StatusBadRequest, "Invalid Rename", err.Error())
		}
		a.auditSvc.Record(r.Context(), reqID, actor, "node.rename", "node", id, "error", ip, map[string]any{"new_name": body.Name})
		return
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "node.rename", string(node.Kind), id, "success", ip, map[string]any{"new_name": node.Name})
	WriteJSON(w, http.StatusOK, node)
}

// Admin POST /api/v1/nodes/{id}/move
func (a *API) HandleMoveNode(w http.ResponseWriter, r *http.Request, id string) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	var body struct {
		NewParentID string `json:"new_parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}
	if body.NewParentID == "" {
		body.NewParentID = files.RootNodeID
	}

	node, err := a.filesSvc.MoveNode(r.Context(), id, body.NewParentID)
	if err != nil {
		if errors.Is(err, files.ErrCycleDetected) {
			WriteProblem(w, r, http.StatusBadRequest, "Cycle Detected", err.Error())
		} else if errors.Is(err, files.ErrAlreadyExists) {
			WriteProblem(w, r, http.StatusConflict, "Name Conflict in Target Folder", err.Error())
		} else if errors.Is(err, files.ErrSystemNodeProtected) {
			WriteProblem(w, r, http.StatusForbidden, "Protected System Node", err.Error())
		} else {
			WriteProblem(w, r, http.StatusBadRequest, "Invalid Move", err.Error())
		}
		a.auditSvc.Record(r.Context(), reqID, actor, "node.move", "node", id, "error", ip, map[string]any{"target_parent": body.NewParentID})
		return
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "node.move", string(node.Kind), id, "success", ip, map[string]any{"target_parent": body.NewParentID})
	WriteJSON(w, http.StatusOK, node)
}

// Admin POST /api/v1/nodes/{id}/trash
func (a *API) HandleTrashNode(w http.ResponseWriter, r *http.Request, id string) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	err := a.filesSvc.TrashNode(r.Context(), id)
	if err != nil {
		if errors.Is(err, files.ErrSystemNodeProtected) {
			WriteProblem(w, r, http.StatusForbidden, "Protected System Node", err.Error())
		} else {
			WriteProblem(w, r, http.StatusInternalServerError, "Trash Failed", err.Error())
		}
		a.auditSvc.Record(r.Context(), reqID, actor, "node.trash", "node", id, "error", ip, nil)
		return
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "node.trash", "node", id, "success", ip, nil)
	WriteJSON(w, http.StatusOK, map[string]string{"status": "trashed"})
}

// Admin GET /api/v1/trash
func (a *API) HandleListTrash(w http.ResponseWriter, r *http.Request) {
	items, err := a.filesSvc.ListTrash(r.Context())
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, "Failed to list trash", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, items)
}

// Admin POST /api/v1/trash/{id}/restore
func (a *API) HandleRestoreNode(w http.ResponseWriter, r *http.Request, id string) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	node, err := a.filesSvc.RestoreNode(r.Context(), id)
	if err != nil {
		if errors.Is(err, files.ErrAlreadyExists) {
			WriteProblem(w, r, http.StatusConflict, "Name Collision at Restore Target", err.Error())
		} else {
			WriteProblem(w, r, http.StatusBadRequest, "Restore Failed", err.Error())
		}
		a.auditSvc.Record(r.Context(), reqID, actor, "node.restore", "node", id, "error", ip, nil)
		return
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "node.restore", string(node.Kind), id, "success", ip, nil)
	WriteJSON(w, http.StatusOK, node)
}

// Admin DELETE /api/v1/trash/{id}
func (a *API) HandlePurgeNode(w http.ResponseWriter, r *http.Request, id string) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	storageKeys, err := a.filesSvc.PurgeNode(r.Context(), id)
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, "Purge Failed", err.Error())
		a.auditSvc.Record(r.Context(), reqID, actor, "node.purge", "node", id, "error", ip, nil)
		return
	}

	// Delete physical storage objects
	for _, key := range storageKeys {
		_ = a.store.RemoveObject(key)
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "node.purge", "node", id, "success", ip, map[string]any{"purged_objects": len(storageKeys)})
	w.WriteHeader(http.StatusNoContent)
}

// Admin GET /api/v1/files/{id}/download
func (a *API) HandleDownloadFile(w http.ResponseWriter, r *http.Request, id string) {
	node, err := a.filesSvc.GetNode(r.Context(), id)
	if err != nil || node.TrashedAt != nil || node.Kind != files.KindFile {
		WriteProblem(w, r, http.StatusNotFound, "File Not Found", "File does not exist or has been trashed")
		return
	}

	if err := a.downloadsSvc.ServeDownload(w, r, node, false); err != nil {
		if errors.Is(err, downloads.ErrDownloadLimitReached) {
			WriteProblem(w, r, http.StatusTooManyRequests, "Download Limit Reached", "Please wait for active downloads to finish")
			return
		}
	}
}

// Admin PUT /api/v1/files/{id}/backup-selection
func (a *API) HandleBackupSelection(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Selected bool `json:"selected"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}

	if err := a.filesSvc.ToggleBackupSelection(r.Context(), id, body.Selected); err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, "Failed to update backup selection", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"selected": body.Selected})
}

// Tus Resumable Upload Handlers
func (a *API) HandleTusOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Tus-Version", "1.0.0")
	w.Header().Set("Tus-Max-Size", strconv.FormatInt(a.cfg.MaxFileBytes, 10))
	w.Header().Set("Tus-Extension", "creation,termination")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) HandleTusCreate(w http.ResponseWriter, r *http.Request) {
	_, actor := a.getContextActor(r)
	lengthHeader := r.Header.Get("Upload-Length")
	if lengthHeader == "" {
		WriteProblem(w, r, http.StatusBadRequest, "Missing Upload-Length", "Upload-Length header is required")
		return
	}
	size, err := strconv.ParseInt(lengthHeader, 10, 64)
	if err != nil || size < 0 {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid Upload-Length", "Must be non-negative integer")
		return
	}

	metadataHeader := r.Header.Get("Upload-Metadata")
	filename := "uploaded_file"
	parentID := files.RootNodeID

	if metadataHeader != "" {
		pairs := strings.Split(metadataHeader, ",")
		for _, pair := range pairs {
			parts := strings.SplitN(strings.TrimSpace(pair), " ", 2)
			if len(parts) == 2 {
				k := parts[0]
				vBytes, _ := base64.StdEncoding.DecodeString(parts[1])
				v := string(vBytes)
				if k == "filename" || k == "name" {
					filename = v
				} else if k == "parent_id" && v != "" {
					parentID = v
				}
			}
		}
	}

	u, err := a.uploadsSvc.CreateUpload(r.Context(), actor, parentID, filename, size)
	if err != nil {
		if errors.Is(err, files.ErrAlreadyExists) {
			WriteProblem(w, r, http.StatusConflict, "File Already Exists", err.Error())
		} else if errors.Is(err, uploads.ErrQuotaExceeded) {
			WriteProblem(w, r, http.StatusInsufficientStorage, "Quota Exceeded", err.Error())
		} else if errors.Is(err, uploads.ErrFileTooLarge) {
			WriteProblem(w, r, http.StatusRequestEntityTooLarge, "File Too Large", err.Error())
		} else {
			WriteProblem(w, r, http.StatusBadRequest, "Upload Creation Failed", err.Error())
		}
		return
	}

	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Location", "/api/v1/uploads/tus/"+u.ID)
	w.WriteHeader(http.StatusCreated)
}

func (a *API) HandleTusHead(w http.ResponseWriter, r *http.Request, id string) {
	u, err := a.uploadsSvc.GetUpload(r.Context(), id)
	if err != nil {
		WriteProblem(w, r, http.StatusNotFound, "Upload Not Found", err.Error())
		return
	}

	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.ReceivedBytes, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(u.ExpectedSize, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

func (a *API) HandleTusPatch(w http.ResponseWriter, r *http.Request, id string) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	contentType := r.Header.Get("Content-Type")
	if contentType != "application/offset+octet-stream" {
		WriteProblem(w, r, http.StatusUnsupportedMediaType, "Unsupported Media Type", "Content-Type must be application/offset+octet-stream")
		return
	}

	offsetHeader := r.Header.Get("Upload-Offset")
	if offsetHeader == "" {
		WriteProblem(w, r, http.StatusBadRequest, "Missing Upload-Offset", "Upload-Offset header is required")
		return
	}
	offset, err := strconv.ParseInt(offsetHeader, 10, 64)
	if err != nil || offset < 0 {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid Upload-Offset", "Must be non-negative integer")
		return
	}

	u, err := a.uploadsSvc.WriteChunk(r.Context(), id, offset, r.Body)
	if err != nil {
		if errors.Is(err, uploads.ErrOffsetMismatch) {
			WriteProblem(w, r, http.StatusConflict, "Offset Mismatch", err.Error())
		} else {
			WriteProblem(w, r, http.StatusBadRequest, "Upload Write Failed", err.Error())
		}
		return
	}

	if u.State == uploads.StateComplete {
		a.auditSvc.Record(r.Context(), reqID, actor, "upload.complete", "file", u.ID, "success", ip, map[string]any{
			"filename": u.Name,
			"size":     u.ExpectedSize,
			"sha256":   u.Sha256,
		})
	}

	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.ReceivedBytes, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) HandleTusDelete(w http.ResponseWriter, r *http.Request, id string) {
	if err := a.uploadsSvc.TerminateUpload(r.Context(), id); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Terminate Failed", err.Error())
		return
	}
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.WriteHeader(http.StatusNoContent)
}

// Shares API
func (a *API) HandleListShares(w http.ResponseWriter, r *http.Request) {
	sharesList, err := a.sharesSvc.ListShares(r.Context())
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, "Failed to list shares", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, sharesList)
}

func (a *API) HandleCreateShare(w http.ResponseWriter, r *http.Request) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	var body struct {
		TargetNodeID string     `json:"target_node_id"`
		Password     string     `json:"password,omitempty"`
		ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}

	res, err := a.sharesSvc.CreateShare(r.Context(), body.TargetNodeID, body.Password, body.ExpiresAt)
	if err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Create Share Failed", err.Error())
		a.auditSvc.Record(r.Context(), reqID, actor, "share.create", "share", "", "error", ip, nil)
		return
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "share.create", "share", res.Share.ID, "success", ip, map[string]any{
		"target_node_id": res.Share.TargetNodeID,
		"has_password":   res.Share.HasPassword,
	})
	WriteJSON(w, http.StatusCreated, res)
}

func (a *API) HandleRevokeShare(w http.ResponseWriter, r *http.Request, id string) {
	reqID, actor := a.getContextActor(r)
	ip := a.clientIP(r)

	if err := a.sharesSvc.RevokeShare(r.Context(), id); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Revoke Failed", err.Error())
		a.auditSvc.Record(r.Context(), reqID, actor, "share.revoke", "share", id, "error", ip, nil)
		return
	}

	a.auditSvc.Record(r.Context(), reqID, actor, "share.revoke", "share", id, "success", ip, nil)
	w.WriteHeader(http.StatusNoContent)
}

// System metrics
func (a *API) HandleSystemStorage(w http.ResponseWriter, r *http.Request) {
	var storedBytes, reservedBytes int64
	_ = a.db.QueryRowContext(r.Context(), "SELECT COALESCE(SUM(size_bytes), 0) FROM file_objects;").Scan(&storedBytes)
	_ = a.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(reservation_bytes), 0) FROM uploads 
		WHERE state IN ('created', 'receiving', 'verifying', 'finalizing');
	`).Scan(&reservedBytes)

	var nodeCount int
	_ = a.db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM nodes WHERE trashed_at IS NULL;").Scan(&nodeCount)

	WriteJSON(w, http.StatusOK, map[string]any{
		"stored_bytes":      storedBytes,
		"reserved_bytes":    reservedBytes,
		"total_used_bytes":  storedBytes + reservedBytes,
		"max_storage_bytes": a.cfg.MaxStorageBytes,
		"node_count":        nodeCount,
	})
}

// Public Share Handlers (/s/{token}*)
func (a *API) HandlePublicShareGet(w http.ResponseWriter, r *http.Request, token string) {
	share, err := a.sharesSvc.GetShareByToken(r.Context(), token)
	if err != nil {
		WriteProblem(w, r, http.StatusNotFound, "Share Not Found", "Link is invalid or has expired")
		return
	}

	// Check cookie
	cookie, _ := r.Cookie("filemgr_share_session")
	unlocked := false
	if cookie != nil {
		unlocked = a.sharesSvc.ValidateSessionCookie(r.Context(), share, cookie.Value)
	}
	if !share.HasPassword {
		unlocked = true
	}

	view := shares.PublicShareView{
		ID:           share.ID,
		TargetName:   share.TargetNode.Name,
		TargetKind:   string(share.TargetNode.Kind),
		NeedPassword: share.HasPassword,
		Unlocked:     unlocked,
		TargetNode:   share.TargetNode,
	}
	if share.TargetNode.FileObject != nil {
		view.SizeBytes = share.TargetNode.FileObject.SizeBytes
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	WriteJSON(w, http.StatusOK, view)
}

func (a *API) HandlePublicShareUnlock(w http.ResponseWriter, r *http.Request, token string) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}

	cookieVal, err := a.sharesSvc.UnlockWithPassword(r.Context(), token, body.Password)
	if err != nil {
		WriteProblem(w, r, http.StatusUnauthorized, "Invalid Password", err.Error())
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "filemgr_share_session",
		Value:    cookieVal,
		Path:     "/s/" + token,
		HttpOnly: true,
		Secure:   a.cfg.AppEnv == "production",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})

	WriteJSON(w, http.StatusOK, map[string]bool{"unlocked": true})
}

func (a *API) HandlePublicShareNodes(w http.ResponseWriter, r *http.Request, token string) {
	share, err := a.sharesSvc.GetShareByToken(r.Context(), token)
	if err != nil {
		WriteProblem(w, r, http.StatusNotFound, "Share Not Found", "Link is invalid or has expired")
		return
	}

	if share.HasPassword {
		cookie, _ := r.Cookie("filemgr_share_session")
		if cookie == nil || !a.sharesSvc.ValidateSessionCookie(r.Context(), share, cookie.Value) {
			WriteProblem(w, r, http.StatusUnauthorized, "Password Required", "Must unlock share with password first")
			return
		}
	}

	if share.TargetNode.Kind != files.KindFolder {
		WriteProblem(w, r, http.StatusBadRequest, "Invalid Share Target", "Shared item is not a folder")
		return
	}

	q := r.URL.Query()
	parentID := q.Get("parent_id")
	targetParent := share.TargetNodeID
	if parentID != "" && parentID != share.TargetNodeID {
		// Verify parentID is within share folder tree
		if _, err := a.sharesSvc.CheckNodeInShare(r.Context(), share, parentID); err != nil {
			WriteProblem(w, r, http.StatusForbidden, "Access Denied", "Folder is outside share")
			return
		}
		targetParent = parentID
	}

	cursor := q.Get("cursor")
	limit, _ := strconv.Atoi(q.Get("limit"))
	search := q.Get("search")

	res, err := a.filesSvc.ListNodes(r.Context(), &targetParent, cursor, limit, search)
	if err != nil {
		WriteProblem(w, r, http.StatusInternalServerError, "List Failed", err.Error())
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	WriteJSON(w, http.StatusOK, res)
}

func (a *API) HandlePublicShareDownload(w http.ResponseWriter, r *http.Request, token, nodeID string) {
	share, err := a.sharesSvc.GetShareByToken(r.Context(), token)
	if err != nil {
		WriteProblem(w, r, http.StatusNotFound, "Share Not Found", "Link is invalid or has expired")
		return
	}

	if share.HasPassword {
		cookie, _ := r.Cookie("filemgr_share_session")
		if cookie == nil || !a.sharesSvc.ValidateSessionCookie(r.Context(), share, cookie.Value) {
			WriteProblem(w, r, http.StatusUnauthorized, "Password Required", "Must unlock share with password first")
			return
		}
	}

	node, err := a.sharesSvc.CheckNodeInShare(r.Context(), share, nodeID)
	if err != nil {
		WriteProblem(w, r, http.StatusForbidden, "Access Denied", err.Error())
		return
	}

	if err := a.downloadsSvc.ServeDownload(w, r, node, true); err != nil {
		if errors.Is(err, downloads.ErrDownloadLimitReached) {
			WriteProblem(w, r, http.StatusTooManyRequests, "Download Limit Reached", "Public download capacity reached")
			return
		}
	}
}
