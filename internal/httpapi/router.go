package httpapi

import (
	"io/fs"
	"net/http"
	"strings"

	"filemgr/internal/observability"
)

func (a *API) Handler(staticFS fs.FS) http.Handler {
	mux := http.NewServeMux()

	// 1. Health checks (always open)
	mux.HandleFunc("GET /health/live", a.HandleHealthLive)
	mux.HandleFunc("GET /health/ready", a.HandleHealthReady)

	// 2. Public share endpoints (/s/{token}*)
	mux.HandleFunc("GET /s/{token}", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		a.HandlePublicShareGet(w, r, token)
	})
	mux.HandleFunc("POST /s/{token}/unlock", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		a.HandlePublicShareUnlock(w, r, token)
	})
	mux.HandleFunc("GET /s/{token}/nodes", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		a.HandlePublicShareNodes(w, r, token)
	})
	mux.HandleFunc("GET /s/{token}/files/{node_id}/download", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		nodeID := r.PathValue("node_id")
		a.HandlePublicShareDownload(w, r, token, nodeID)
	})

	// 3. Admin endpoints (protected by Cloudflare Access verifier)
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /api/v1/me", a.HandleGetMe)
	adminMux.HandleFunc("GET /api/v1/nodes", a.HandleListNodes)
	adminMux.HandleFunc("POST /api/v1/folders", a.HandleCreateFolder)
	adminMux.HandleFunc("PATCH /api/v1/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.HandleRenameNode(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("POST /api/v1/nodes/{id}/move", func(w http.ResponseWriter, r *http.Request) {
		a.HandleMoveNode(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("POST /api/v1/nodes/{id}/trash", func(w http.ResponseWriter, r *http.Request) {
		a.HandleTrashNode(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("GET /api/v1/trash", a.HandleListTrash)
	adminMux.HandleFunc("POST /api/v1/trash/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		a.HandleRestoreNode(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("DELETE /api/v1/trash/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.HandlePurgeNode(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("GET /api/v1/files/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		a.HandleDownloadFile(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("PUT /api/v1/files/{id}/backup-selection", func(w http.ResponseWriter, r *http.Request) {
		a.HandleBackupSelection(w, r, r.PathValue("id"))
	})

	// Tus endpoints
	adminMux.HandleFunc("OPTIONS /api/v1/uploads/tus", a.HandleTusOptions)
	adminMux.HandleFunc("POST /api/v1/uploads/tus", a.HandleTusCreate)
	adminMux.HandleFunc("HEAD /api/v1/uploads/tus/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.HandleTusHead(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("PATCH /api/v1/uploads/tus/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.HandleTusPatch(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("DELETE /api/v1/uploads/tus/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.HandleTusDelete(w, r, r.PathValue("id"))
	})

	// Shares management
	adminMux.HandleFunc("GET /api/v1/shares", a.HandleListShares)
	adminMux.HandleFunc("POST /api/v1/shares", a.HandleCreateShare)
	adminMux.HandleFunc("DELETE /api/v1/shares/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.HandleRevokeShare(w, r, r.PathValue("id"))
	})
	adminMux.HandleFunc("GET /api/v1/system/storage", a.HandleSystemStorage)

	protectedAdmin := a.authVerifier.Middleware(adminMux)

	// Static file handler for embedded UI
	var staticServer http.Handler
	if staticFS != nil {
		fileServer := http.FileServer(http.FS(staticFS))
		staticServer = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}
			f, err := staticFS.Open(path)
			if err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
			// SPA fallback to index.html
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
		})
	} else {
		staticServer = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<!DOCTYPE html><html><head><title>File Manager</title></head><body><h1>Self-hosted File Manager</h1><p>Backend API operational.</p></body></html>`))
		})
	}

	// Host routing & security middleware
	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if idx := strings.Index(host, ":"); idx != -1 {
			host = host[:idx]
		}

		adminHost := a.cfg.AppAdminHost
		if idx := strings.Index(adminHost, ":"); idx != -1 {
			adminHost = adminHost[:idx]
		}

		shareHost := a.cfg.AppShareHost
		if idx := strings.Index(shareHost, ":"); idx != -1 {
			shareHost = shareHost[:idx]
		}

		// Security Headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none';")

		// If accessed via share host: allow ONLY /s/*, /health/*, and static share UI
		if a.cfg.AppEnv == "production" && host == shareHost {
			if strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/health/") {
				mux.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1/") {
				http.NotFound(w, r)
				return
			}
			// Static assets for public share
			staticServer.ServeHTTP(w, r)
			return
		}

		// Origin check for state-changing requests
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			origin := r.Header.Get("Origin")
			if origin != "" && a.cfg.AppEnv == "production" {
				// Origin must match expected hosts
				if !strings.Contains(origin, adminHost) && !strings.Contains(origin, shareHost) {
					http.Error(w, `{"error":"forbidden","message":"Cross-origin request rejected"}`, http.StatusForbidden)
					return
				}
			}
		}

		// Route /api/v1/*
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			protectedAdmin.ServeHTTP(w, r)
			return
		}

		// Route /health/* or /s/*
		if strings.HasPrefix(r.URL.Path, "/health/") || strings.HasPrefix(r.URL.Path, "/s/") {
			mux.ServeHTTP(w, r)
			return
		}

		// Static assets
		staticServer.ServeHTTP(w, r)
	})

	return observability.RequestLogger(rootHandler)
}
