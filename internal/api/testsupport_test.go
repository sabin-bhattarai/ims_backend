//go:build integration

// Package api_test drives the assembled application over HTTP against a real
// Postgres, which is the only way to prove the parts agree: the ledger's row
// locks, the FIFO layers, the RBAC middleware and the migrations all have to be
// real for these tests to mean anything.
//
// Run with: go test -tags=integration ./...
// Requires Docker (testcontainers starts Postgres; Redis is intentionally
// absent, which also exercises the degraded-cache path).
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/sabin-bhattarai/ims-backend/internal/api"
	"github.com/sabin-bhattarai/ims-backend/internal/platform"
)

// harness is a running application wired to a throwaway database.
type harness struct {
	app    *api.Application
	t      *testing.T
	tokens map[string]string // role name → access token
}

// newHarness starts Postgres, migrates it and builds the application.
func newHarness(t *testing.T) *harness {
	t.Helper()

	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("ims_test"),
		postgres.WithUsername("ims"),
		postgres.WithPassword("ims"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	cfg := platform.Config{
		Env:               "test",
		Port:              0,
		LogLevel:          "error",
		ShutdownTimeout:   5 * time.Second,
		DatabaseURL:       dsn,
		DBMaxOpenConns:    10,
		DBMaxIdleConns:    2,
		AutoMigrate:       true,
		RedisURL:          "redis://localhost:1/0", // deliberately unreachable
		RedisNamespace:    "ims-test",
		JWTSecret:         "integration-test-secret-value-long-enough",
		JWTIssuer:         "ims-test",
		AccessTokenTTL:    15 * time.Minute,
		RefreshTokenTTL:   24 * time.Hour,
		PasswordResetTTL:  time.Hour,
		BcryptCost:        4, // fastest allowed; these tests hash a lot
		RateLimitRequests: 10000,
		RateLimitWindow:   time.Minute,
		CORSOrigins:       "*",
		WebAppBaseURL:     "http://localhost:5173",
	}

	lg := zerolog.New(io.Discard)

	db, err := platform.NewDatabase(ctx, cfg, lg)
	require.NoError(t, err)
	require.NoError(t, platform.RunMigrations(db, lg))

	cache := platform.NewCache(ctx, cfg, lg)
	queue, err := platform.NewQueue(cfg, lg)
	require.NoError(t, err)

	app := api.New(api.Deps{DB: db, Cache: cache, Queue: queue, Config: cfg, Logger: lg})

	return &harness{app: app, t: t, tokens: map[string]string{}}
}

// response is a decoded API reply.
type response struct {
	Status int
	Body   map[string]any
}

// data returns the `data` object from a success envelope.
func (r response) data() map[string]any {
	obj, _ := r.Body["data"].(map[string]any)
	return obj
}

// dataList returns the `data` array from a list envelope.
func (r response) dataList() []any {
	list, _ := r.Body["data"].([]any)
	return list
}

// errorCode returns the machine-readable code from an error envelope.
func (r response) errorCode() string {
	errObj, ok := r.Body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

// do issues a request against the app. An empty token sends no Authorization
// header, which is how the unauthenticated cases are exercised.
func (h *harness) do(method, path, token string, payload any) response {
	h.t.Helper()

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		require.NoError(h.t, err)
		body = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// Generous timeout: the first call in a suite pays for bcrypt and
	// connection setup.
	resp, err := h.app.Fiber.Test(req, 30*1000)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)

	out := response{Status: resp.StatusCode}
	if len(raw) > 0 {
		// A 204 has no body; anything else in this API is JSON.
		if err := json.Unmarshal(raw, &out.Body); err != nil {
			h.t.Fatalf("%s %s: response was not JSON (status %d): %s", method, path, resp.StatusCode, raw)
		}
	}
	return out
}

// register creates the demo organization and returns the admin's access token.
func (h *harness) register(orgName, email string) string {
	h.t.Helper()

	resp := h.do(http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"organization_name": orgName,
		"full_name":         "Test Admin",
		"email":             email,
		"password":          "SuperSecret123!",
	})
	require.Equal(h.t, http.StatusCreated, resp.Status, "register: %v", resp.Body)

	tokens, ok := resp.data()["tokens"].(map[string]any)
	require.True(h.t, ok, "register response had no tokens: %v", resp.Body)
	access, _ := tokens["access_token"].(string)
	require.NotEmpty(h.t, access)

	h.tokens["admin"] = access
	return access
}

// addUser creates a user with the given role and returns their access token,
// so RBAC can be asserted from the perspective of each role.
func (h *harness) addUser(adminToken string, role, email string) string {
	h.t.Helper()

	resp := h.do(http.MethodPost, "/api/v1/users", adminToken, map[string]any{
		"full_name": "Test " + role,
		"email":     email,
		"password":  "SuperSecret123!",
		"role":      role,
	})
	require.Equal(h.t, http.StatusCreated, resp.Status, "create %s: %v", role, resp.Body)

	login := h.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": email, "password": "SuperSecret123!",
	})
	require.Equal(h.t, http.StatusOK, login.Status, "login %s: %v", role, login.Body)

	tokens, _ := login.data()["tokens"].(map[string]any)
	access, _ := tokens["access_token"].(string)
	require.NotEmpty(h.t, access)

	h.tokens[role] = access
	return access
}

// id pulls a string id out of a response's data object.
func (h *harness) id(resp response) string {
	h.t.Helper()
	value, ok := resp.data()["id"].(string)
	require.True(h.t, ok, "response had no id: %v", resp.Body)
	return value
}

// num reads a numeric field, which JSON decodes as float64.
func num(t *testing.T, obj map[string]any, key string) float64 {
	t.Helper()
	value, ok := obj[key].(float64)
	require.True(t, ok, "field %q was not a number in %v", key, obj)
	return value
}

// uniqueEmail keeps parallel tests from colliding on the global email index.
func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@example.test", prefix, time.Now().UnixNano())
}
