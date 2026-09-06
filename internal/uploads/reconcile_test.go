package uploads

import (
	"context"
	"os"
	"testing"

	"filemgr/internal/files"
)

func TestReconcilerCrashRecovery(t *testing.T) {
	_, store, filesSvc, uploadsSvc, cleanup := setupUploadTestEnv(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Simulate crash in Phase B: Upload state is 'finalizing', staging file exists, but final object doesn't yet
	content := []byte("Content from interrupted upload before crash")
	u, err := uploadsSvc.CreateUpload(ctx, "admin@example.com", files.RootNodeID, "crash_recovered.txt", int64(len(content)))
	if err != nil {
		t.Fatalf("create upload failed: %v", err)
	}

	// Write content into staging
	stagingPath, _ := store.StagingPath(u.StagingKey)
	if err := os.WriteFile(stagingPath, content, 0600); err != nil {
		t.Fatalf("failed to write staging file: %v", err)
	}

	// Simulate state set to 'finalizing' before a crash
	_, err = uploadsSvc.db.ExecContext(ctx, `
		UPDATE uploads 
		SET state = 'finalizing', received_bytes = ? 
		WHERE id = ?;
	`, int64(len(content)), u.ID)
	if err != nil {
		t.Fatalf("failed simulating crash state: %v", err)
	}

	// Run Reconciler
	if err := uploadsSvc.Reconcile(ctx); err != nil {
		t.Fatalf("reconciler failed: %v", err)
	}

	// Verify upload transitioned to 'complete' and reservation freed
	recoveredUpload, err := uploadsSvc.GetUpload(ctx, u.ID)
	if err != nil {
		t.Fatalf("get recovered upload failed: %v", err)
	}
	if recoveredUpload.State != StateComplete {
		t.Errorf("expected state complete after reconcile, got %s", recoveredUpload.State)
	}
	if recoveredUpload.ReservationBytes != 0 {
		t.Errorf("expected reservation 0, got %d", recoveredUpload.ReservationBytes)
	}

	// Verify file node now exists in tree
	listRes, err := filesSvc.ListNodes(ctx, nil, "", 10, "")
	if err != nil {
		t.Fatalf("list nodes failed: %v", err)
	}
	found := false
	for _, it := range listRes.Items {
		if it.Name == "crash_recovered.txt" {
			found = true
			if it.FileObject == nil || it.FileObject.SizeBytes != int64(len(content)) {
				t.Errorf("node fileObject mismatch")
			}
		}
	}
	if !found {
		t.Errorf("recovered node not found in file tree")
	}
}
