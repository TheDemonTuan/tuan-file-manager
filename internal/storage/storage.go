package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

var (
	ErrObjectNotFound  = errors.New("object not found")
	ErrStagingNotFound = errors.New("staging file not found")
	ErrInvalidPath     = errors.New("invalid storage path")
)

type Storage struct {
	baseDir    string
	objectsDir string
	stagingDir string
}

func NewStorage(baseDir string) (*Storage, error) {
	cleanBase := filepath.Clean(baseDir)
	objectsDir := filepath.Join(cleanBase, "objects")
	stagingDir := filepath.Join(cleanBase, "staging", "uploads")
	dbDir := filepath.Join(cleanBase, "db")

	for _, dir := range []string{cleanBase, objectsDir, stagingDir, dbDir} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("failed to create directory %q: %w", dir, err)
		}
	}

	return &Storage{
		baseDir:    cleanBase,
		objectsDir: objectsDir,
		stagingDir: stagingDir,
	}, nil
}

func (s *Storage) BaseDir() string {
	return s.baseDir
}

func (s *Storage) GenerateOpaqueKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Storage) ObjectPath(key string) (string, error) {
	if len(key) < 4 || filepath.Base(key) != key {
		return "", ErrInvalidPath
	}
	shard := key[:2]
	return filepath.Join(s.objectsDir, shard, key), nil
}

func (s *Storage) StagingPath(stagingKey string) (string, error) {
	if len(stagingKey) < 4 || filepath.Base(stagingKey) != stagingKey {
		return "", ErrInvalidPath
	}
	return filepath.Join(s.stagingDir, stagingKey), nil
}

func (s *Storage) CreateStagingFile(stagingKey string) (*os.File, error) {
	path, err := s.StagingPath(stagingKey)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
}

func (s *Storage) OpenStagingForAppend(stagingKey string) (*os.File, error) {
	path, err := s.StagingPath(stagingKey)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
}

func (s *Storage) RemoveStagingFile(stagingKey string) error {
	path, err := s.StagingPath(stagingKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Storage) MoveStagingToObject(stagingKey, objectKey string) error {
	stagingPath, err := s.StagingPath(stagingKey)
	if err != nil {
		return err
	}
	objPath, err := s.ObjectPath(objectKey)
	if err != nil {
		return err
	}

	destDir := filepath.Dir(objPath)
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return fmt.Errorf("failed to create object shard directory %q: %w", destDir, err)
	}

	// Sync staging file before moving
	f, err := os.Open(stagingPath)
	if err == nil {
		_ = f.Sync()
		_ = f.Close()
	}

	if err := os.Rename(stagingPath, objPath); err != nil {
		return fmt.Errorf("atomic rename failed: %w", err)
	}

	return nil
}

func (s *Storage) OpenObject(objectKey string) (*os.File, error) {
	path, err := s.ObjectPath(objectKey)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrObjectNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *Storage) RemoveObject(objectKey string) error {
	path, err := s.ObjectPath(objectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Storage) SniffMimeType(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "application/octet-stream", err
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "application/octet-stream", err
	}
	if n == 0 {
		return "application/octet-stream", nil
	}
	return http.DetectContentType(buf[:n]), nil
}

func (s *Storage) StagingExists(stagingKey string) bool {
	path, err := s.StagingPath(stagingKey)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *Storage) ObjectExists(objectKey string) bool {
	path, err := s.ObjectPath(objectKey)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
