package uploads

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemgr/internal/config"
	"filemgr/internal/db"
	"filemgr/internal/files"
	"filemgr/internal/storage"
)

func setupUploadTestEnv(t *testing.T) (*db.DB, *storage.Storage, *files.Service, *Service, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "filemgr-uploads-test-*")
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
		MaxStorageBytes: 10 * 1024 * 1024, // 10 MB for test
		MaxFileBytes:    5 * 1024 * 1024,  // 5 MB
		UploadExpiry:    1 * time.Hour,
	}

	filesSvc := files.NewService(database)
	uploadsSvc := NewService(database, store, filesSvc, cfg)

	cleanup := func() {
		database.Close()
		os.RemoveAll(tempDir)
	}

	return database, store, filesSvc, uploadsSvc, cleanup
}

func TestUploadLifecycleAndFinalization(t *testing.T) {
	_, store, filesSvc, uploadsSvc, cleanup := setupUploadTestEnv(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Create upload session
	content := []byte("Hello, self-hosted file manager with tus resumable upload!")
	size := int64(len(content))

	u, err := uploadsSvc.CreateUpload(ctx, "admin@example.com", files.RootNodeID, "greeting.txt", size)
	if err != nil {
		t.Fatalf("failed to create upload: %v", err)
	}

	if u.State != StateCreated {
		t.Errorf("expected state created, got %s", u.State)
	}
	if u.ReservationBytes != size {
		t.Errorf("expected reservation %d, got %d", size, u.ReservationBytes)
	}

	// 2. Chunk 1: first half
	half := size / 2
	part1 := content[:half]
	part2 := content[half:]

	uAfterPart1, err := uploadsSvc.WriteChunk(ctx, u.ID, 0, bytes.NewReader(part1))
	if err != nil {
		t.Fatalf("failed writing chunk 1: %v", err)
	}
	if uAfterPart1.ReceivedBytes != int64(len(part1)) {
		t.Errorf("expected received %d, got %d", len(part1), uAfterPart1.ReceivedBytes)
	}
	if uAfterPart1.State != StateReceiving {
		t.Errorf("expected state receiving, got %s", uAfterPart1.State)
	}

	// 3. Reject wrong offset
	_, err = uploadsSvc.WriteChunk(ctx, u.ID, 0, bytes.NewReader(part2))
	if err != ErrOffsetMismatch {
		t.Errorf("expected ErrOffsetMismatch on offset 0, got %v", err)
	}

	// 4. Chunk 2: second half -> triggers finalization
	uAfterPart2, err := uploadsSvc.WriteChunk(ctx, u.ID, int64(len(part1)), bytes.NewReader(part2))
	if err != nil {
		t.Fatalf("failed writing chunk 2: %v", err)
	}
	if uAfterPart2.State != StateComplete {
		t.Errorf("expected state complete after final chunk, got %s", uAfterPart2.State)
	}
	if uAfterPart2.ReservationBytes != 0 {
		t.Errorf("expected reservation freed, got %d", uAfterPart2.ReservationBytes)
	}
	if uAfterPart2.Sha256 == nil || *uAfterPart2.Sha256 == "" {
		t.Errorf("expected sha256 checksum, got nil")
	}

	// 5. Verify node and file_object exist in files service
	listRes, err := filesSvc.ListNodes(ctx, nil, "", 10, "")
	if err != nil {
		t.Fatalf("list nodes failed: %v", err)
	}
	if len(listRes.Items) != 1 {
		t.Fatalf("expected 1 file node, found %d", len(listRes.Items))
	}
	fileNode := listRes.Items[0]
	if fileNode.Name != "greeting.txt" {
		t.Errorf("expected file name greeting.txt, got %s", fileNode.Name)
	}
	if fileNode.FileObject == nil {
		t.Fatalf("expected fileObject attached")
	}
	if fileNode.FileObject.SizeBytes != size {
		t.Errorf("expected file size %d, got %d", size, fileNode.FileObject.SizeBytes)
	}

	// 6. Verify physical storage file exists and matches content
	objFile, err := store.OpenObject(fileNode.FileObject.StorageKey)
	if err != nil {
		t.Fatalf("failed to open final object: %v", err)
	}
	defer objFile.Close()

	readBuf := make([]byte, size)
	n, _ := objFile.Read(readBuf)
	if int64(n) != size || !bytes.Equal(readBuf, content) {
		t.Errorf("read content did not match written content: %q vs %q", string(readBuf), string(content))
	}
}

func TestUploadQuotaAndTermination(t *testing.T) {
	_, _, _, uploadsSvc, cleanup := setupUploadTestEnv(t)
	defer cleanup()

	ctx := context.Background()

	// Try reserving more than max file size
	_, err := uploadsSvc.CreateUpload(ctx, "admin@example.com", files.RootNodeID, "huge.bin", 6*1024*1024)
	if err != ErrFileTooLarge {
		t.Errorf("expected ErrFileTooLarge, got %v", err)
	}

	// Create and terminate
	u, err := uploadsSvc.CreateUpload(ctx, "admin@example.com", files.RootNodeID, "cancel.bin", 1024)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := uploadsSvc.TerminateUpload(ctx, u.ID); err != nil {
		t.Fatalf("terminate failed: %v", err)
	}

	uTerm, err := uploadsSvc.GetUpload(ctx, u.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if uTerm.State != StateTerminated {
		t.Errorf("expected state terminated, got %s", uTerm.State)
	}
}
