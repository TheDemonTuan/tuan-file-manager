package shares

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"filemgr/internal/config"
	"filemgr/internal/db"
	"filemgr/internal/files"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

type Service struct {
	db       *db.DB
	filesSvc *files.Service
	cfg      *config.Config
}

func NewService(db *db.DB, filesSvc *files.Service, cfg *config.Config) *Service {
	return &Service{
		db:       db,
		filesSvc: filesSvc,
		cfg:      cfg,
	}
}

func (s *Service) hashToken(token string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.ShareTokenPepper))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 1, 64*1024, 2, 32)
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=1,p=2$%s$%s", b64Salt, b64Hash), nil
}

func (s *Service) verifyPassword(password, encodedHash string) bool {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	computedHash := argon2.IDKey([]byte(password), salt, 1, 64*1024, 2, uint32(len(expectedHash)))
	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1
}

func (s *Service) CreateShare(ctx context.Context, targetNodeID string, password string, expiresAt *time.Time) (*CreateShareResult, error) {
	node, err := s.filesSvc.GetNode(ctx, targetNodeID)
	if err != nil {
		return nil, err
	}
	if node.TrashedAt != nil || node.IsSystem {
		return nil, files.ErrInvalidOperation
	}

	// Generate 256 bits (32 bytes) CSPRNG token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("failed to generate secure share token: %w", err)
	}
	tokenHex := hex.EncodeToString(tokenBytes)
	tokenHash := s.hashToken(tokenHex)

	var passwordHash *string
	if password != "" {
		h, err := s.hashPassword(password)
		if err != nil {
			return nil, fmt.Errorf("failed to hash password: %w", err)
		}
		passwordHash = &h
	}

	shareID := uuid.New().String()
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	var expStr *string
	if expiresAt != nil {
		str := expiresAt.UTC().Format(time.RFC3339)
		expStr = &str
	}

	query := `
		INSERT INTO shares (
			id, target_node_id, token_hash, password_hash, expires_at,
			revoked_at, auth_version, global_share_epoch, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, NULL, 1, ?, ?, ?);
	`
	_, err = s.db.ExecContext(ctx, query,
		shareID, targetNodeID, tokenHash, passwordHash, expStr,
		s.cfg.GlobalShareEpoch, nowStr, nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create share: %w", err)
	}

	share, err := s.GetShareByID(ctx, shareID)
	if err != nil {
		return nil, err
	}

	return &CreateShareResult{
		Share: share,
		Token: tokenHex, // Only returned once at creation!
	}, nil
}

func (s *Service) GetShareByID(ctx context.Context, id string) (*Share, error) {
	query := `
		SELECT id, target_node_id, token_hash, password_hash, expires_at,
		       revoked_at, auth_version, global_share_epoch, created_at, updated_at
		FROM shares WHERE id = ?;
	`
	return s.scanShare(ctx, s.db.QueryRowContext(ctx, query, id))
}

func (s *Service) GetShareByToken(ctx context.Context, token string) (*Share, error) {
	tokenHash := s.hashToken(token)
	query := `
		SELECT id, target_node_id, token_hash, password_hash, expires_at,
		       revoked_at, auth_version, global_share_epoch, created_at, updated_at
		FROM shares WHERE token_hash = ?;
	`
	share, err := s.scanShare(ctx, s.db.QueryRowContext(ctx, query, tokenHash))
	if err != nil {
		return nil, err
	}

	if share.RevokedAt != nil {
		return nil, ErrShareRevoked
	}
	if share.ExpiresAt != nil && time.Now().UTC().After(*share.ExpiresAt) {
		return nil, ErrShareExpired
	}
	if share.GlobalShareEpoch != s.cfg.GlobalShareEpoch {
		return nil, ErrShareRevoked
	}

	return share, nil
}

func (s *Service) ListShares(ctx context.Context) ([]Share, error) {
	query := `
		SELECT id, target_node_id, token_hash, password_hash, expires_at,
		       revoked_at, auth_version, global_share_epoch, created_at, updated_at
		FROM shares 
		ORDER BY created_at DESC;
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shares []Share
	for rows.Next() {
		sh, err := s.scanShare(ctx, rows)
		if err != nil {
			return nil, err
		}
		shares = append(shares, *sh)
	}
	return shares, nil
}

func (s *Service) RevokeShare(ctx context.Context, id string) error {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		UPDATE shares 
		SET revoked_at = ?, auth_version = auth_version + 1, updated_at = ?
		WHERE id = ?;
	`, nowStr, nowStr, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrShareNotFound
	}
	return nil
}

func (s *Service) UnlockWithPassword(ctx context.Context, token, password string) (string, error) {
	share, err := s.GetShareByToken(ctx, token)
	if err != nil {
		return "", err
	}
	if share.PasswordHash == nil || *share.PasswordHash == "" {
		return "", errors.New("share does not require a password")
	}

	if !s.verifyPassword(password, *share.PasswordHash) {
		return "", ErrInvalidPassword
	}

	// Generate signed session cookie token
	expiry := time.Now().UTC().Add(24 * time.Hour).Unix()
	return s.createSessionToken(share.ID, share.AuthVersion, share.GlobalShareEpoch, expiry), nil
}

func (s *Service) ValidateSessionCookie(ctx context.Context, share *Share, cookieValue string) bool {
	if share.PasswordHash == nil || *share.PasswordHash == "" {
		return true // No password needed
	}
	if cookieValue == "" {
		return false
	}

	parts := strings.Split(cookieValue, ".")
	if len(parts) != 5 {
		return false
	}
	shareID := parts[0]
	authVerStr := parts[1]
	epochStr := parts[2]
	expiryStr := parts[3]
	signature := parts[4]

	if shareID != share.ID {
		return false
	}

	authVer, err := strconv.Atoi(authVerStr)
	if err != nil || authVer != share.AuthVersion {
		return false
	}

	epoch, err := strconv.ParseInt(epochStr, 10, 64)
	if err != nil || epoch != share.GlobalShareEpoch {
		return false
	}

	exp, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil || time.Now().UTC().Unix() > exp {
		return false
	}

	payload := fmt.Sprintf("%s.%s.%s.%s", shareID, authVerStr, epochStr, expiryStr)
	mac := hmac.New(sha256.New, []byte(s.cfg.ShareSessionSecret))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(signature), []byte(expectedSig)) == 1
}

func (s *Service) createSessionToken(shareID string, authVersion int, epoch int64, expiry int64) string {
	payload := fmt.Sprintf("%s.%d.%d.%d", shareID, authVersion, epoch, expiry)
	mac := hmac.New(sha256.New, []byte(s.cfg.ShareSessionSecret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s.%s", payload, sig)
}

// CheckNodeInShare verifies that target node is the share root or a descendant of the share root folder
func (s *Service) CheckNodeInShare(ctx context.Context, share *Share, nodeID string) (*files.Node, error) {
	node, err := s.filesSvc.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if node.TrashedAt != nil {
		return nil, files.ErrNotFound
	}

	if node.ID == share.TargetNodeID {
		return node, nil
	}

	// Check if node is a descendant of share.TargetNodeID
	var inTree int
	query := `
		WITH RECURSIVE subnodes(id) AS (
			SELECT id FROM nodes WHERE parent_id = ?
			UNION ALL
			SELECT n.id FROM nodes n JOIN subnodes s ON n.parent_id = s.id
		)
		SELECT COUNT(1) FROM subnodes WHERE id = ?;
	`
	err = s.db.QueryRowContext(ctx, query, share.TargetNodeID, nodeID).Scan(&inTree)
	if err != nil || inTree == 0 {
		return nil, ErrAccessDenied
	}

	return node, nil
}

func (s *Service) scanShare(ctx context.Context, sc filesScanner) (*Share, error) {
	var (
		id, targetNodeID, tokenHash, createdStr, updatedStr string
		pwdHash, expStr, revStr                             sql.NullString
		authVer                                             int
		epoch                                               int64
	)
	err := sc.Scan(
		&id, &targetNodeID, &tokenHash, &pwdHash, &expStr,
		&revStr, &authVer, &epoch, &createdStr, &updatedStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrShareNotFound
		}
		return nil, err
	}

	sh := &Share{
		ID:               id,
		TargetNodeID:     targetNodeID,
		TokenHash:        tokenHash,
		AuthVersion:      authVer,
		GlobalShareEpoch: epoch,
	}
	if pwdHash.Valid {
		sh.PasswordHash = &pwdHash.String
		sh.HasPassword = true
	}
	if expStr.Valid {
		if t, err := time.Parse(time.RFC3339, expStr.String); err == nil {
			sh.ExpiresAt = &t
		}
	}
	if revStr.Valid {
		if t, err := time.Parse(time.RFC3339, revStr.String); err == nil {
			sh.RevokedAt = &t
		}
	}
	sh.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	sh.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)

	// Fetch target node info
	if targetNode, err := s.filesSvc.GetNode(ctx, targetNodeID); err == nil {
		sh.TargetNode = targetNode
	}

	return sh, nil
}

type filesScanner interface {
	Scan(dest ...any) error
}
