// Package middleware holds the HTTP middleware chain: request ids, structured
// access logs, panic recovery, error rendering, authentication, authorisation
// and rate limiting.
package middleware

import (
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
)

// RequestIDHeader is echoed on every response and included in every log line,
// which is what makes a report of "my stock adjustment failed" traceable.
const RequestIDHeader = "X-Request-ID"

const requestIDKey = "ims.request_id"

// RequestID assigns or propagates a request id.
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(RequestIDHeader)
		if id == "" || len(id) > 64 {
			id = uuid.NewString()
		}
		c.Locals(requestIDKey, id)
		c.Set(RequestIDHeader, id)
		return c.Next()
	}
}

// RequestIDOf returns the current request id.
func RequestIDOf(c *fiber.Ctx) string {
	id, _ := c.Locals(requestIDKey).(string)
	return id
}

// AccessLog emits one structured line per request.
func AccessLog(lg zerolog.Logger) fiber.Handler {
	lg = lg.With().Str("component", "http").Logger()
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()

		event := lg.Info()
		switch {
		case status >= 500:
			event = lg.Error()
		case status >= 400:
			event = lg.Warn()
		}

		event.
			Str("request_id", RequestIDOf(c)).
			Str("method", c.Method()).
			Str("path", c.Path()).
			Int("status", status).
			Dur("latency", time.Since(start)).
			Str("ip", c.IP())

		if id := auth.MustIdentity(c); id.UserID != uuid.Nil {
			event.Str("user_id", id.UserID.String()).Str("org_id", id.OrgID.String())
		}
		event.Msg("request")
		return err
	}
}

// ErrorHandler renders every error as the standard error envelope. It is
// installed as Fiber's app-level handler, so a handler simply returns an error
// and never writes a failure response itself.
//
// Internal details are logged but never sent to the client: a leaked SQL string
// or file path is a reconnaissance gift.
func ErrorHandler(lg zerolog.Logger) fiber.ErrorHandler {
	lg = lg.With().Str("component", "http").Logger()

	return func(c *fiber.Ctx, err error) error {
		appErr := shared.AsError(err)

		if appErr == nil {
			var fiberErr *fiber.Error
			if errors.As(err, &fiberErr) {
				switch fiberErr.Code {
				case fiber.StatusNotFound:
					appErr = shared.NotFound("route")
				case fiber.StatusMethodNotAllowed:
					appErr = shared.Validation("method not allowed")
				case fiber.StatusRequestEntityTooLarge:
					appErr = shared.Validation("request body too large")
				default:
					appErr = shared.Internal(fiberErr.Message)
				}
			} else {
				appErr = shared.Internal("an unexpected error occurred").WithCause(err)
			}
		}

		status := appErr.Status()
		if status >= 500 {
			lg.Error().Err(err).
				Str("request_id", RequestIDOf(c)).
				Str("path", c.Path()).
				Str("method", c.Method()).
				Msg("request failed")
		} else {
			lg.Debug().Err(err).
				Str("request_id", RequestIDOf(c)).
				Str("path", c.Path()).
				Msg("request rejected")
		}

		// Rebuild the client-facing error so a wrapped cause cannot leak.
		return c.Status(status).JSON(fiber.Map{
			"error": fiber.Map{
				"code":       appErr.Code,
				"message":    appErr.Message,
				"details":    appErr.Details,
				"request_id": RequestIDOf(c),
			},
		})
	}
}

// Recover converts a panic into a 500 through the normal error path, so one bad
// request cannot take the process down.
func Recover(lg zerolog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) (err error) {
		defer func() {
			if r := recover(); r != nil {
				lg.Error().
					Interface("panic", r).
					Str("request_id", RequestIDOf(c)).
					Str("path", c.Path()).
					Bytes("stack", stack()).
					Msg("recovered from panic")
				err = shared.Internal("an unexpected error occurred")
			}
		}()
		return c.Next()
	}
}

// RequireAuth validates the bearer token and stores the caller identity.
func RequireAuth(tokens *auth.TokenIssuer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get(fiber.HeaderAuthorization)
		if header == "" {
			return shared.Unauthorized("authorization header is required")
		}
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return shared.Unauthorized("authorization header must be a Bearer token")
		}

		identity, err := tokens.Verify(strings.TrimSpace(parts[1]))
		if err != nil {
			return err
		}
		auth.StoreIdentity(c, identity)
		return c.Next()
	}
}

// RequirePermission guards a route by capability. Mount it after RequireAuth.
func RequirePermission(perm auth.Permission) fiber.Handler {
	return func(c *fiber.Ctx) error {
		identity, err := auth.FromContext(c)
		if err != nil {
			return err
		}
		if !identity.Can(perm) {
			return shared.Forbidden("your role does not permit this action").
				WithDetails(map[string]any{"required_permission": string(perm)})
		}
		return c.Next()
	}
}

// RequireRole guards a route by minimum role rank. Prefer RequirePermission;
// this exists for a few genuinely role-shaped checks such as user management.
func RequireRole(minimum auth.Role) fiber.Handler {
	return func(c *fiber.Ctx) error {
		identity, err := auth.FromContext(c)
		if err != nil {
			return err
		}
		if !identity.Role.AtLeast(minimum) {
			return shared.Forbidden("your role does not permit this action").
				WithDetails(map[string]any{"required_role": string(minimum)})
		}
		return c.Next()
	}
}

// Meta builds the request fingerprint recorded on sessions and audit rows.
func Meta(c *fiber.Ctx) auth.RequestMeta {
	return auth.RequestMeta{
		IP:        c.IP(),
		UserAgent: c.Get(fiber.HeaderUserAgent),
		RequestID: RequestIDOf(c),
	}
}

// stack captures a bounded stack trace for panic logging.
func stack() []byte {
	buf := make([]byte, 8<<10)
	return buf[:runtime.Stack(buf, false)]
}

// Actor builds the audit actor for the current caller.
func Actor(c *fiber.Ctx) shared.Actor {
	id := auth.MustIdentity(c)
	return shared.Actor{
		ID: id.UserID, Email: id.Email, OrgID: id.OrgID,
		IP: c.IP(), RequestID: RequestIDOf(c),
	}
}
