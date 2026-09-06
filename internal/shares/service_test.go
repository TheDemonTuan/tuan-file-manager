package shares

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemgr/internal/config"
	"filemgr/internal/db"
	"filemgr/internal/files"
)

func setupSharesTestEnv(t *testing.T) (*db.DB, *files.Service, *Service, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "filemgr-shares-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath, 2*time.Second, "NORMAL")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	cfg := &config.Config{
		ShareTokenPepper:   "test-token-pepper",
		ShareSessionSecret: "test-session-secret-key-123456",
		GlobalShareEpoch:   1,
	}

	filesSvc := files.NewService(database)
	sharesSvc := NewService(database, filesSvc, cfg)

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}
	return database, filesSvc, sharesSvc, cleanup
}

func TestShareCreationPasswordAndRevocation(t *testing.T) {
	_, filesSvc, sharesSvc, cleanup := setupSharesTestEnv(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Create a folder and child folder
	sharedFolder, err := filesSvc.CreateFolder(ctx, nil, "PublicDocs")
	if err != nil {
		t.Fatalf("create folder failed: %v", err)
	}
	insideFolder, err := filesSvc.CreateFolder(ctx, &sharedFolder.ID, "Subfolder")
	if err != nil {
		t.Fatalf("create child folder failed: %v", err)
	}
	outsideFolder, err := filesSvc.CreateFolder(ctx, nil, "PrivateDocs")
	if err != nil {
		t.Fatalf("create outside folder failed: %v", err)
	}

	// 2. Create password-protected share
	res, err := sharesSvc.CreateShare(ctx, sharedFolder.ID, "supersecret", nil)
	if err != nil {
		t.Fatalf("create share failed: %v", err)
	}
	if res.Token == "" {
		t.Fatalf("expected plaintext token in result")
	}

	// 3. Resolve share by token
	share, err := sharesSvc.GetShareByToken(ctx, res.Token)
	if err != nil {
		t.Fatalf("get share by token failed: %v", err)
	}
	if !share.HasPassword {
		t.Errorf("expected share to have password")
	}

	// 4. Try unlock with wrong password
	_, err = sharesSvc.UnlockWithPassword(ctx, res.Token, "wrongpassword")
	if err != ErrInvalidPassword {
		t.Errorf("expected ErrInvalidPassword, got %v", err)
	}

	// 5. Unlock with correct password
	cookieVal, err := sharesSvc.UnlockWithPassword(ctx, res.Token, "supersecret")
	if err != nil {
		t.Fatalf("unlock failed: %v", err)
	}

	// 6. Validate session cookie
	if !sharesSvc.ValidateSessionCookie(ctx, share, cookieVal) {
		t.Errorf("expected session cookie to be valid")
	}

	// 7. Test subtree boundary: Subfolder is allowed, PrivateDocs is denied
	_, err = sharesSvc.CheckNodeInShare(ctx, share, insideFolder.ID)
	if err != nil {
		t.Errorf("expected insideFolder to be allowed, got: %v", err)
	}

	_, err = sharesSvc.CheckNodeInShare(ctx, share, outsideFolder.ID)
	if err != ErrAccessDenied {
		t.Errorf("expected ErrAccessDenied for outsideFolder, got: %v", err)
	}

	// 8. Revoke share and verify immediate rejection
	if err := sharesSvc.RevokeShare(ctx, share.ID); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}

	_, err = sharesSvc.GetShareByToken(ctx, res.Token)
	if err != ErrShareRevoked {
		t.Errorf("expected ErrShareRevoked, got: %v", err)
	}

	// Invalidate session cookie
	updatedShare, err := sharesSvc.GetShareByID(ctx, share.ID)
	if err != nil {
		t.Fatalf("get updated share failed: %v", err)
	}
	if sharesSvc.ValidateSessionCookie(ctx, updatedShare, cookieVal) {
		t.Errorf("expected cookie to become invalid after auth_version bump")
	}
}
