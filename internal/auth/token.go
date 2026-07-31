package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/sabin-bhattarai/ims_backend/internal/platform"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// Claims is the access-token payload. Organization and role travel in the
// token so authorisation needs no database round trip on every request; the
// short access TTL bounds how stale a revoked role can be.
type Claims struct {
	jwt.RegisteredClaims
	OrgID uuid.UUID `json:"org"`
	Email string    `json:"email"`
	Role  Role      `json:"role"`
	// SessionID ties the access token to the refresh token that minted it, so
	// revoking a session invalidates its lineage.
	SessionID string `json:"sid"`
}

// TokenPair is what a successful login or refresh returns.
type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type" example:"Bearer"`
	ExpiresIn    int       `json:"expires_in" example:"900"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// TokenIssuer mints and verifies tokens.
type TokenIssuer struct {
	secret     []byte
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewTokenIssuer builds an issuer from configuration.
func NewTokenIssuer(cfg platform.Config) *TokenIssuer {
	return &TokenIssuer{
		secret:     []byte(cfg.JWTSecret),
		issuer:     cfg.JWTIssuer,
		accessTTL:  cfg.AccessTokenTTL,
		refreshTTL: cfg.RefreshTokenTTL,
	}
}

// RefreshTTL exposes the configured refresh lifetime.
func (t *TokenIssuer) RefreshTTL() time.Duration { return t.refreshTTL }

// AccessToken signs a short-lived access token for a user.
func (t *TokenIssuer) AccessToken(u User, sessionID string) (string, time.Time, error) {
	now := time.Now().UTC()
	exp := now.Add(t.accessTTL)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.String(),
			Issuer:    t.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        uuid.NewString(),
		},
		OrgID:     u.OrganizationID,
		Email:     u.Email,
		Role:      u.Role,
		SessionID: sessionID,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, exp, nil
}

// Verify parses and validates an access token, returning the caller identity.
func (t *TokenIssuer) Verify(token string) (Identity, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(tk *jwt.Token) (any, error) {
		// Pin the algorithm: accepting whatever the token declares is how
		// "alg: none" forgeries get in.
		if _, ok := tk.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", tk.Header["alg"])
		}
		return t.secret, nil
	}, jwt.WithIssuer(t.issuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !parsed.Valid {
		return Identity{}, shared.Unauthorized("invalid or expired token").WithCause(err)
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return Identity{}, shared.Unauthorized("malformed token subject").WithCause(err)
	}
	if !claims.Role.Valid() {
		return Identity{}, shared.Unauthorized("token carries an unknown role")
	}

	return Identity{
		UserID:    userID,
		OrgID:     claims.OrgID,
		Email:     claims.Email,
		Role:      claims.Role,
		SessionID: claims.SessionID,
	}, nil
}

// NewOpaqueToken generates a 256-bit refresh token and its storage hash. The
// plaintext is returned to the client once and never persisted.
func NewOpaqueToken() (plaintext, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, HashToken(plaintext), nil
}

// HashToken returns the hex-encoded SHA-256 of a token. SHA-256 (not bcrypt)
// is correct here: the input is already 256 bits of entropy, so there is
// nothing to brute-force, and lookups must be indexable.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
