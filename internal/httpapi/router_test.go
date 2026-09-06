package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemgr/internal/audit"
	"filemgr/internal/auth"
	"filemgr/internal/config"
	"filemgr/internal/db"
	"filemgr/internal/downloads"
	"filemgr/internal/files"
	"filemgr/internal/shares"
	"filemgr/internal/storage"
	"filemgr/internal/uploads"
)

func setupTestServer(t *testing.T) (*API, http.Handler, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "filemgr-api-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath, 2*time.Second, "NORMAL")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	store, err := storage.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	cfg := &config.Config{
		AppEnv:             "development",
		AppAdminHost:       "admin.example.com",
		AppShareHost:       "share.example.com",
		MaxStorageBytes:    100 * 1024 * 1024,
		MaxFileBytes:       50 * 1024 * 1024,
		TusChunkSize:       16 * 1024 * 1024,
		MaxActiveDownloads: 4,
		MaxPublicDownloads: 2,
		UploadExpiry:       1 * time.Hour,
		DevAuthBypass:      true,
		DevAdminEmail:      "admin@example.com",
		ShareTokenPepper:   "test-pepper",
		ShareSessionSecret: "test-secret",
		GlobalShareEpoch:   1,
	}

	filesSvc := files.NewService(database)
	uploadsSvc := uploads.NewService(database, store, filesSvc, cfg)
	downloadsSvc := downloads.NewService(store, filesSvc, cfg)
	sharesSvc := shares.NewService(database, filesSvc, cfg)
	auditSvc := audit.NewService(database)
	authVerifier := auth.NewVerifier(cfg)

	api := NewAPI(cfg, database, filesSvc, uploadsSvc, downloadsSvc, sharesSvc, auditSvc, authVerifier, store)
	handler := api.Handler(nil)

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}
	return api, handler, cleanup
}

func TestCompleteAPIFlow(t *testing.T) {
	_, handler, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Health check
	req := httptest.NewRequest("GET", "/health/live", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health check status: %d", w.Code)
	}

	// 2. Identity me
	req = httptest.NewRequest("GET", "/api/v1/me", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("me endpoint status: %d", w.Code)
	}

	// 3. Create folder
	createFolderBody := `{"name":"Reports"}`
	req = httptest.NewRequest("POST", "/api/v1/folders", bytes.NewBufferString(createFolderBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create folder status: %d, body: %s", w.Code, w.Body.String())
	}
	var folder files.Node
	_ = json.Unmarshal(w.Body.Bytes(), &folder)
	if folder.Name != "Reports" {
		t.Errorf("expected folder Reports, got %s", folder.Name)
	}

	// 4. Tus upload to Reports folder
	content := []byte("Financial Q3 Report: Revenue up 40%!")
	meta := fmt.Sprintf("filename %s,parent_id %s",
		base64.StdEncoding.EncodeToString([]byte("q3_report.txt")),
		base64.StdEncoding.EncodeToString([]byte(folder.ID)),
	)
	req = httptest.NewRequest("POST", "/api/v1/uploads/tus", nil)
	req.Header.Set("Upload-Length", fmt.Sprintf("%d", len(content)))
	req.Header.Set("Upload-Metadata", meta)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("tus create status: %d, body: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if location == "" {
		t.Fatalf("missing Location header in tus create")
	}

	// Tus HEAD
	req = httptest.NewRequest("HEAD", location, nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("tus head status: %d", w.Code)
	}
	if w.Header().Get("Upload-Offset") != "0" {
		t.Errorf("expected initial offset 0, got %s", w.Header().Get("Upload-Offset"))
	}

	// Tus PATCH
	req = httptest.NewRequest("PATCH", location, bytes.NewReader(content))
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "0")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("tus patch status: %d, body: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Upload-Offset") != fmt.Sprintf("%d", len(content)) {
		t.Errorf("expected offset %d, got %s", len(content), w.Header().Get("Upload-Offset"))
	}

	// 5. List items in Reports folder
	req = httptest.NewRequest("GET", "/api/v1/nodes?parent_id="+folder.ID, nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list nodes status: %d", w.Code)
	}
	var listRes files.ListNodesResult
	_ = json.Unmarshal(w.Body.Bytes(), &listRes)
	if len(listRes.Items) != 1 {
		t.Fatalf("expected 1 file in Reports folder, got %d", len(listRes.Items))
	}
	fileNode := listRes.Items[0]
	if fileNode.Name != "q3_report.txt" {
		t.Errorf("expected file name q3_report.txt, got %s", fileNode.Name)
	}

	// 6. Download file with HTTP Range (bytes=0-8)
	req = httptest.NewRequest("GET", "/api/v1/files/"+fileNode.ID+"/download", nil)
	req.Header.Set("Range", "bytes=0-8")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent {
		t.Errorf("expected 206 Partial Content, got %d", w.Code)
	}
	expectedRangeContent := string(content[0:9])
	if w.Body.String() != expectedRangeContent {
		t.Errorf("expected range content %q, got %q", expectedRangeContent, w.Body.String())
	}

	// 7. Create Share link for the file with a password
	shareReqBody := fmt.Sprintf(`{"target_node_id":"%s","password":"mypassword123"}`, fileNode.ID)
	req = httptest.NewRequest("POST", "/api/v1/shares", bytes.NewBufferString(shareReqBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create share status: %d, body: %s", w.Code, w.Body.String())
	}
	var shareRes shares.CreateShareResult
	_ = json.Unmarshal(w.Body.Bytes(), &shareRes)
	token := shareRes.Token
	if token == "" {
		t.Fatalf("expected token in create share result")
	}

	// 8. Public Share: GET /s/{token}
	req = httptest.NewRequest("GET", "/s/"+token, nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("public share get status: %d", w.Code)
	}
	var publicView shares.PublicShareView
	_ = json.Unmarshal(w.Body.Bytes(), &publicView)
	if !publicView.NeedPassword || publicView.Unlocked {
		t.Errorf("expected locked password share")
	}

	// 9. Public Share: Unlock with password
	unlockBody := `{"password":"mypassword123"}`
	req = httptest.NewRequest("POST", "/s/"+token+"/unlock", bytes.NewBufferString(unlockBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unlock status: %d, body: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "filemgr_share_session" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("missing filemgr_share_session cookie")
	}

	// 10. Public Share: Download file using session cookie
	req = httptest.NewRequest("GET", "/s/"+token+"/files/"+fileNode.ID+"/download", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("public download status: %d, body: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), content) {
		t.Errorf("downloaded content mismatch")
	}

	// 11. Storage metrics
	req = httptest.NewRequest("GET", "/api/v1/system/storage", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("system storage status: %d", w.Code)
	}
}
