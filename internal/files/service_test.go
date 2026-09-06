package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemgr/internal/db"
)

func setupTestDB(t *testing.T) (*db.DB, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "filemgr-files-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath, 2*time.Second, "NORMAL")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}
	return database, cleanup
}

func TestTreeOperationsAndInvariants(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	svc := NewService(database)

	// 1. Create root folder "Documents"
	docFolder, err := svc.CreateFolder(ctx, nil, "Documents")
	if err != nil {
		t.Fatalf("failed to create folder: %v", err)
	}
	if docFolder.Name != "Documents" {
		t.Errorf("expected name Documents, got %s", docFolder.Name)
	}

	// 2. Duplicate sibling rejection
	_, err = svc.CreateFolder(ctx, nil, "documents")
	if err != ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists on case-folded duplicate, got %v", err)
	}

	// 3. Create subfolder "Work" inside "Documents"
	workFolder, err := svc.CreateFolder(ctx, &docFolder.ID, "Work")
	if err != nil {
		t.Fatalf("failed to create subfolder: %v", err)
	}

	// 4. Create subfolder "Projects" inside "Work"
	projFolder, err := svc.CreateFolder(ctx, &workFolder.ID, "Projects")
	if err != nil {
		t.Fatalf("failed to create nested subfolder: %v", err)
	}

	// 5. Cycle protection: Attempt to move "Documents" into "Projects"
	_, err = svc.MoveNode(ctx, docFolder.ID, projFolder.ID)
	if err != ErrCycleDetected {
		t.Errorf("expected ErrCycleDetected when moving ancestor into descendant, got %v", err)
	}

	// 6. Rename folder
	renamed, err := svc.RenameNode(ctx, projFolder.ID, "Alpha Projects")
	if err != nil {
		t.Fatalf("failed to rename folder: %v", err)
	}
	if renamed.Name != "Alpha Projects" {
		t.Errorf("expected renamed name Alpha Projects, got %s", renamed.Name)
	}

	// 7. Search active items
	searchRes, err := svc.ListNodes(ctx, nil, "", 10, "alpha")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(searchRes.Items) != 1 || searchRes.Items[0].ID != projFolder.ID {
		t.Errorf("expected to find Alpha Projects in search, got %+v", searchRes.Items)
	}

	// 8. Trash "Documents" (which cascades to Work and Projects)
	if err := svc.TrashNode(ctx, docFolder.ID); err != nil {
		t.Fatalf("failed to trash folder: %v", err)
	}

	// 9. Verify trashed descendants do not leak into active search!
	searchAfterTrash, err := svc.ListNodes(ctx, nil, "", 10, "alpha")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(searchAfterTrash.Items) != 0 {
		t.Errorf("expected trashed descendant not to appear in search, found %d items", len(searchAfterTrash.Items))
	}

	// 10. List trash items
	trashItems, err := svc.ListTrash(ctx)
	if err != nil {
		t.Fatalf("list trash failed: %v", err)
	}
	if len(trashItems) != 1 || trashItems[0].ID != docFolder.ID {
		t.Errorf("expected 1 trash item (Documents), got %d", len(trashItems))
	}

	// 11. Restore "Documents"
	restored, err := svc.RestoreNode(ctx, docFolder.ID)
	if err != nil {
		t.Fatalf("failed to restore node: %v", err)
	}
	if restored.TrashedAt != nil {
		t.Errorf("expected TrashedAt to be nil after restore")
	}

	// 12. Search again: descendant should be searchable again
	searchAfterRestore, err := svc.ListNodes(ctx, nil, "", 10, "alpha")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(searchAfterRestore.Items) != 1 {
		t.Errorf("expected Alpha Projects to be searchable after restore, found %d items", len(searchAfterRestore.Items))
	}

	// 13. System node protection
	if err := svc.TrashNode(ctx, RootNodeID); err != ErrSystemNodeProtected {
		t.Fatalf("expected ErrSystemNodeProtected, got: %v", err)
	}
}
