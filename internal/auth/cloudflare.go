package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"filemgr/internal/config"
	"filemgr/internal/observability"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrUnauthorized = errors.New("unauthorized: missing or invalid Cloudflare Access credentials")
	ErrForbidden    = errors.New("forbidden: identity not authorized")
)

type JWKSKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type JWKSResponse struct {
	Keys []JWKSKey `json:"keys"`
}

type Verifier struct {
	cfg        *config.Config
	httpClient *http.Client
	mu         sync.RWMutex
	cachedKeys map[string]*rsa.PublicKey
	lastFetch  time.Time
}

func NewVerifier(cfg *config.Config) *Verifier {
	return &Verifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		cachedKeys: make(map[string]*rsa.PublicKey),
	}
}

func (v *Verifier) fetchJWKS(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Rate limit JWKS fetches to once every 10 seconds
	if time.Since(v.lastFetch) < 10*time.Second && len(v.cachedKeys) > 0 {
		return nil
	}

	url := fmt.Sprintf("https://%s/cdn-cgi/access/certs", v.cfg.CFAccessTeamDomain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch jwks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected jwks status: %d", resp.StatusCode)
	}

	var jwks JWKSResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("failed to decode jwks: %w", err)
	}

	newKeys := make(map[string]*rsa.PublicKey)
	for _, key := range jwks.Keys {
		if key.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil {
			continue
		}
		var eInt int
		for _, b := range eBytes {
			eInt = (eInt << 8) | int(b)
		}

		pubKey := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: eInt,
		}
		newKeys[key.Kid] = pubKey
	}

	v.cachedKeys = newKeys
	v.lastFetch = time.Now()
	return nil
}

func (v *Verifier) VerifyToken(ctx context.Context, tokenString string) (string, error) {
	if v.cfg.DevAuthBypass {
		return v.cfg.DevAdminEmail, nil
	}

	if tokenString == "" {
		return "", ErrUnauthorized
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}

		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, errors.New("missing kid in token header")
		}

		v.mu.RLock()
		pubKey, exists := v.cachedKeys[kid]
		v.mu.RUnlock()

		if !exists {
			if err := v.fetchJWKS(ctx); err != nil {
				return nil, err
			}
			v.mu.RLock()
			pubKey, exists = v.cachedKeys[kid]
			v.mu.RUnlock()
			if !exists {
				return nil, errors.New("signing key not found in jwks")
			}
		}

		return pubKey, nil
	})

	if err != nil || !token.Valid {
		return "", ErrUnauthorized
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", ErrUnauthorized
	}

	// Verify audience
	if aud, ok := claims["aud"]; ok {
		var audSlice []string
		switch a := aud.(type) {
		case string:
			audSlice = []string{a}
		case []interface{}:
			for _, item := range a {
				if s, ok := item.(string); ok {
					audSlice = append(audSlice, s)
				}
			}
		}
		matched := false
		for _, a := range audSlice {
			if a == v.cfg.CFAccessAud {
				matched = true
				break
			}
		}
		if !matched {
			return "", ErrUnauthorized
		}
	} else {
		return "", ErrUnauthorized
	}

	// Extract email or subject
	var identity string
	if email, ok := claims["email"].(string); ok && email != "" {
		identity = email
	} else if sub, ok := claims["sub"].(string); ok {
		identity = sub
	}

	if identity == "" {
		return "", ErrUnauthorized
	}

	if v.cfg.CFAccessAllowedEmail != "" && identity != v.cfg.CFAccessAllowedEmail {
		return "", ErrForbidden
	}

	return identity, nil
}

func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v.cfg.DevAuthBypass {
			ctx := context.WithValue(r.Context(), observability.IdentityKey, v.cfg.DevAdminEmail)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Cloudflare Access passes token in header Cf-Access-Jwt-Assertion
		token := r.Header.Get("Cf-Access-Jwt-Assertion")
		if token == "" {
			// Fallback to cookie CF_Authorization
			if c, err := r.Cookie("CF_Authorization"); err == nil {
				token = c.Value
			}
		}

		identity, err := v.VerifyToken(r.Context(), token)
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				http.Error(w, `{"error":"forbidden","message":"Identity not permitted"}`, http.StatusForbidden)
				return
			}
			http.Error(w, `{"error":"unauthorized","message":"Cloudflare Access authentication required"}`, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), observability.IdentityKey, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
