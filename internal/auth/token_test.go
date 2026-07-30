package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/platform"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
)

const testSecret = "test-secret-that-is-long-enough-for-hs256"

func testConfig() platform.Config {
	return platform.Config{
		JWTSecret:       testSecret,
		JWTIssuer:       "ims-test",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 720 * time.Hour,
	}
}

func testUser() auth.User {
	u := auth.User{Email: "staff@example.test", Role: auth.RoleWarehouseStaff, Status: auth.StatusActive}
	u.ID = uuid.New()
	u.OrganizationID = uuid.New()
	return u
}

func TestAccessTokenRoundTrip(t *testing.T) {
	t.Parallel()

	issuer := auth.NewTokenIssuer(testConfig())
	user := testUser()
	sessionID := uuid.NewString()

	token, exp, err := issuer.AccessToken(user, sessionID)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().UTC().Add(15*time.Minute), exp, 5*time.Second)

	identity, err := issuer.Verify(token)
	require.NoError(t, err)

	// Organization and role travel in the token so authorisation needs no
	// database read; losing either would silently break tenant isolation.
	assert.Equal(t, user.ID, identity.UserID)
	assert.Equal(t, user.OrganizationID, identity.OrgID)
	assert.Equal(t, user.Email, identity.Email)
	assert.Equal(t, auth.RoleWarehouseStaff, identity.Role)
	assert.Equal(t, sessionID, identity.SessionID)
}

func TestVerifyRejectsBadTokens(t *testing.T) {
	t.Parallel()

	issuer := auth.NewTokenIssuer(testConfig())
	user := testUser()
	valid, _, err := issuer.AccessToken(user, uuid.NewString())
	require.NoError(t, err)

	t.Run("garbage", func(t *testing.T) {
		t.Parallel()
		_, err := issuer.Verify("not-a-jwt")
		requireUnauthorized(t, err)
	})

	t.Run("tampered payload", func(t *testing.T) {
		t.Parallel()
		parts := strings.Split(valid, ".")
		require.Len(t, parts, 3)
		// Flip the signature: the claims stay plausible but the HMAC will not
		// verify.
		_, err := issuer.Verify(parts[0] + "." + parts[1] + ".AAAA" + parts[2][4:])
		requireUnauthorized(t, err)
	})

	t.Run("signed with a different secret", func(t *testing.T) {
		t.Parallel()
		other := auth.NewTokenIssuer(platform.Config{
			JWTSecret: "a-completely-different-secret-value-here",
			JWTIssuer: "ims-test", AccessTokenTTL: time.Minute,
		})
		foreign, _, err := other.AccessToken(user, uuid.NewString())
		require.NoError(t, err)

		_, err = issuer.Verify(foreign)
		requireUnauthorized(t, err)
	})

	t.Run("wrong issuer", func(t *testing.T) {
		t.Parallel()
		cfg := testConfig()
		cfg.JWTIssuer = "somebody-else"
		foreign := auth.NewTokenIssuer(cfg)
		token, _, err := foreign.AccessToken(user, uuid.NewString())
		require.NoError(t, err)

		_, err = issuer.Verify(token)
		requireUnauthorized(t, err)
	})

	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		cfg := testConfig()
		cfg.AccessTokenTTL = -time.Minute // already expired when minted
		expiredIssuer := auth.NewTokenIssuer(cfg)
		token, _, err := expiredIssuer.AccessToken(user, uuid.NewString())
		require.NoError(t, err)

		_, err = issuer.Verify(token)
		requireUnauthorized(t, err)
	})

	t.Run("unsigned alg none forgery", func(t *testing.T) {
		t.Parallel()
		// The classic JWT attack: re-declare the algorithm as "none" and drop
		// the signature. Verify pins HS256, so this must be rejected.
		claims := jwt.MapClaims{
			"sub": user.ID.String(), "iss": "ims-test",
			"exp": time.Now().Add(time.Hour).Unix(),
			"org": user.OrganizationID.String(), "role": "admin",
		}
		unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)

		_, err = issuer.Verify(unsigned)
		requireUnauthorized(t, err)
	})

	t.Run("unknown role in claims", func(t *testing.T) {
		t.Parallel()
		rogue := user
		rogue.Role = auth.Role("superuser")
		token, _, err := issuer.AccessToken(rogue, uuid.NewString())
		require.NoError(t, err)

		_, err = issuer.Verify(token)
		requireUnauthorized(t, err)
	})
}

func TestOpaqueTokenAndHashing(t *testing.T) {
	t.Parallel()

	plaintext, hash, err := auth.NewOpaqueToken()
	require.NoError(t, err)

	assert.NotEmpty(t, plaintext)
	assert.Len(t, hash, 64, "sha-256 hex digest is 64 characters")
	assert.NotEqual(t, plaintext, hash, "the plaintext must never be stored")
	assert.Equal(t, hash, auth.HashToken(plaintext), "hashing must be deterministic for lookups")

	// Two tokens must never collide; a repeat would let one session
	// impersonate another.
	other, otherHash, err := auth.NewOpaqueToken()
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, other)
	assert.NotEqual(t, hash, otherHash)
}

func TestRefreshTokenActive(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	active := auth.RefreshToken{ExpiresAt: now.Add(time.Hour)}
	assert.True(t, active.Active())

	expired := auth.RefreshToken{ExpiresAt: now.Add(-time.Hour)}
	assert.False(t, expired.Active())

	revoked := auth.RefreshToken{ExpiresAt: now.Add(time.Hour), RevokedAt: &now}
	assert.False(t, revoked.Active(), "a revoked token must never be usable, even before expiry")
}

func TestUserCanLogin(t *testing.T) {
	t.Parallel()

	assert.True(t, auth.User{Status: auth.StatusActive}.CanLogin())
	assert.False(t, auth.User{Status: auth.StatusSuspended}.CanLogin())
	assert.False(t, auth.User{Status: auth.StatusInvited}.CanLogin())
}

// requireUnauthorized asserts the error is a 401 application error rather than
// a bare failure, since the middleware relies on the typed code.
func requireUnauthorized(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	appErr := shared.AsError(err)
	require.NotNil(t, appErr, "expected a shared.Error, got %T", err)
	assert.Equal(t, shared.CodeUnauthorized, appErr.Code)
}
