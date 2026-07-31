package shared_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// parseQueryFromURL spins up a throwaway Fiber handler so ParseQuery is
// exercised against a real request rather than a hand-built context.
func parseQueryFromURL(t *testing.T, target string) shared.Query {
	t.Helper()

	var captured shared.Query
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		captured = shared.ParseQuery(c)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return captured
}

func TestParseQueryDefaults(t *testing.T) {
	t.Parallel()

	q := parseQueryFromURL(t, "/test")
	assert.Equal(t, 1, q.Page)
	assert.Equal(t, 25, q.PerPage)
	assert.Empty(t, q.Search)
	assert.Empty(t, q.Sort)
	assert.Empty(t, q.Filters)
}

func TestParseQueryClampsAndIgnoresGarbage(t *testing.T) {
	t.Parallel()

	t.Run("per_page is capped", func(t *testing.T) {
		t.Parallel()
		// An uncapped per_page is a trivial denial-of-service: one request that
		// asks for a million rows.
		q := parseQueryFromURL(t, "/test?per_page=100000")
		assert.Equal(t, 200, q.PerPage)
	})

	t.Run("unparseable values fall back rather than erroring", func(t *testing.T) {
		t.Parallel()
		q := parseQueryFromURL(t, "/test?page=abc&per_page=xyz")
		assert.Equal(t, 1, q.Page)
		assert.Equal(t, 25, q.PerPage)
	})

	t.Run("negative page is ignored", func(t *testing.T) {
		t.Parallel()
		q := parseQueryFromURL(t, "/test?page=-5")
		assert.Equal(t, 1, q.Page)
	})
}

func TestParseQuerySortAndFilters(t *testing.T) {
	t.Parallel()

	q := parseQueryFromURL(t, "/test?sort=-created_at,name&q=widget&filter[status]=active&filter[warehouse_id]=w1")

	require.Len(t, q.Sort, 2)
	assert.Equal(t, "created_at", q.Sort[0].Column)
	assert.True(t, q.Sort[0].Desc, "a leading dash means descending")
	assert.Equal(t, "name", q.Sort[1].Column)
	assert.False(t, q.Sort[1].Desc)

	assert.Equal(t, "widget", q.Search)
	assert.Equal(t, "active", q.Filters["status"])
	assert.Equal(t, "w1", q.Filters["warehouse_id"])
}

func TestQueryOffset(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, shared.Query{Page: 1, PerPage: 25}.Offset())
	assert.Equal(t, 25, shared.Query{Page: 2, PerPage: 25}.Offset())
	assert.Equal(t, 100, shared.Query{Page: 3, PerPage: 50}.Offset())
}

func TestNewPageMeta(t *testing.T) {
	t.Parallel()

	cases := []struct {
		total      int64
		perPage    int
		wantPages  int
	}{
		{total: 0, perPage: 25, wantPages: 0},
		{total: 1, perPage: 25, wantPages: 1},
		{total: 25, perPage: 25, wantPages: 1},
		{total: 26, perPage: 25, wantPages: 2}, // the partial last page must count
		{total: 100, perPage: 10, wantPages: 10},
	}

	for _, tc := range cases {
		meta := shared.NewPageMeta(shared.Query{Page: 1, PerPage: tc.perPage}, tc.total)
		assert.Equal(t, tc.wantPages, meta.TotalPages, "total=%d per_page=%d", tc.total, tc.perPage)
		assert.Equal(t, tc.total, meta.Total)
	}
}

func TestErrorStatusMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err        *shared.Error
		wantStatus int
		wantCode   shared.Code
	}{
		{shared.Validation("bad"), http.StatusBadRequest, shared.CodeValidation},
		{shared.Unauthorized("nope"), http.StatusUnauthorized, shared.CodeUnauthorized},
		{shared.Forbidden("nope"), http.StatusForbidden, shared.CodeForbidden},
		{shared.NotFound("product"), http.StatusNotFound, shared.CodeNotFound},
		{shared.Conflict("clash"), http.StatusConflict, shared.CodeConflict},
		{shared.RateLimited("slow down"), http.StatusTooManyRequests, shared.CodeRateLimited},
		{shared.Internal("boom"), http.StatusInternalServerError, shared.CodeInternal},
		{shared.Unavailable("down"), http.StatusServiceUnavailable, shared.CodeUnavailable},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.wantStatus, tc.err.Status(), "code %s", tc.err.Code)
		assert.Equal(t, tc.wantCode, tc.err.Code)
	}
}

func TestNotFoundMessageNamesTheEntity(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "product not found", shared.NotFound("product").Message)
}

func TestInsufficientStockCarriesQuantities(t *testing.T) {
	t.Parallel()

	// The UI shows the shortfall, so these details are part of the contract.
	err := shared.InsufficientStock(3, 10)
	assert.Equal(t, shared.CodeInsufficientStock, err.Code)
	assert.Equal(t, http.StatusConflict, err.Status())
	assert.Equal(t, 3.0, err.Details["available"])
	assert.Equal(t, 10.0, err.Details["requested"])
}

func TestInvalidTransitionDescribesBothStates(t *testing.T) {
	t.Parallel()

	err := shared.InvalidTransition("received", "cancelled")
	assert.Equal(t, shared.CodeInvalidTransition, err.Code)
	assert.Contains(t, err.Message, "received")
	assert.Contains(t, err.Message, "cancelled")
}

func TestErrorCauseIsWrappedButNotSerialised(t *testing.T) {
	t.Parallel()

	inner := assertErr("connection refused")
	err := shared.Internal("could not reach the database").WithCause(inner)

	// The cause is available for logs via the error chain.
	assert.ErrorIs(t, err, inner)
	// ...but the client-facing message says nothing about internals.
	assert.Equal(t, "could not reach the database", err.Message)

	extracted := shared.AsError(err)
	require.NotNil(t, extracted)
	assert.Equal(t, shared.CodeInternal, extracted.Code)
}

func TestAsErrorReturnsNilForForeignErrors(t *testing.T) {
	t.Parallel()
	assert.Nil(t, shared.AsError(assertErr("some other failure")))
	assert.Nil(t, shared.AsError(nil))
}

func TestValidateReportsJSONFieldNames(t *testing.T) {
	t.Parallel()

	type payload struct {
		Email    string `json:"email"    validate:"required,email"`
		Quantity int    `json:"quantity" validate:"gt=0"`
	}

	err := shared.Validate(&payload{Email: "not-an-email", Quantity: 0})
	require.Error(t, err)

	appErr := shared.AsError(err)
	require.NotNil(t, appErr)
	assert.Equal(t, shared.CodeValidation, appErr.Code)

	// Errors must be keyed by the name the client sent, not the Go field name,
	// so the web form can highlight the right input.
	assert.Contains(t, appErr.Details, "email")
	assert.Contains(t, appErr.Details, "quantity")
	assert.Equal(t, "must be a valid email address", appErr.Details["email"])
}

func TestValidatePassesOnGoodInput(t *testing.T) {
	t.Parallel()

	type payload struct {
		Email string `json:"email" validate:"required,email"`
	}
	assert.NoError(t, shared.Validate(&payload{Email: "ops@example.test"}))
}

// assertErr is a tiny helper producing a plain error value.
type stringError string

func (e stringError) Error() string { return string(e) }

func assertErr(msg string) error { return stringError(msg) }
