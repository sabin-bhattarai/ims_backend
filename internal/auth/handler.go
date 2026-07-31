package auth

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// Handler exposes the auth and user-management HTTP endpoints.
type Handler struct {
	svc *Service
	// requestMeta is injected so the handler does not import the middleware
	// package (which imports this one).
	requestMeta func(*fiber.Ctx) RequestMeta
}

// NewHandler builds the auth handler.
func NewHandler(svc *Service, requestMeta func(*fiber.Ctx) RequestMeta) *Handler {
	return &Handler{svc: svc, requestMeta: requestMeta}
}

// SessionResponse is returned by register, login and refresh.
type SessionResponse struct {
	User        *User        `json:"user"`
	Tokens      *TokenPair   `json:"tokens"`
	Permissions []Permission `json:"permissions"`
}

// MeResponse is returned by GET /auth/me. Permissions are included so the web
// and mobile clients build navigation from the server's answer rather than
// re-deriving the RBAC matrix and drifting from it.
type MeResponse struct {
	User        *User        `json:"user"`
	Permissions []Permission `json:"permissions"`
}

// refreshRequest accepts the refresh token in the body. The web client relies
// on the httpOnly cookie instead and may send an empty body.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Register creates a new organization and its first admin user.
//
//	@Summary		Register a new organization
//	@Description	Creates an organization and its first admin user, returning a session.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			payload	body		RegisterInput	true	"Registration details"
//	@Success		201		{object}	shared.Envelope{data=SessionResponse}
//	@Failure		400		{object}	shared.ErrorEnvelope
//	@Failure		409		{object}	shared.ErrorEnvelope
//	@Router			/auth/register [post]
func (h *Handler) Register(c *fiber.Ctx) error {
	var in RegisterInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	user, tokens, err := h.svc.Register(c.UserContext(), in, h.requestMeta(c))
	if err != nil {
		return err
	}
	setRefreshCookie(c, tokens)
	return shared.Created(c, SessionResponse{
		User: user, Tokens: tokens, Permissions: PermissionsFor(user.Role),
	})
}

// Login authenticates a user.
//
//	@Summary		Log in
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			payload	body		LoginInput	true	"Credentials"
//	@Success		200		{object}	shared.Envelope{data=SessionResponse}
//	@Failure		401		{object}	shared.ErrorEnvelope
//	@Router			/auth/login [post]
func (h *Handler) Login(c *fiber.Ctx) error {
	var in LoginInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	user, tokens, err := h.svc.Login(c.UserContext(), in, h.requestMeta(c))
	if err != nil {
		return err
	}
	setRefreshCookie(c, tokens)
	return shared.OK(c, SessionResponse{
		User: user, Tokens: tokens, Permissions: PermissionsFor(user.Role),
	})
}

// Refresh rotates the refresh token and returns a new access token.
//
//	@Summary		Refresh a session
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			payload	body		refreshRequest	false	"Refresh token (omit when using the cookie)"
//	@Success		200		{object}	shared.Envelope{data=SessionResponse}
//	@Failure		401		{object}	shared.ErrorEnvelope
//	@Router			/auth/refresh [post]
func (h *Handler) Refresh(c *fiber.Ctx) error {
	var in refreshRequest
	_ = c.BodyParser(&in) // an empty body is valid; the cookie is the fallback
	token := in.RefreshToken
	if token == "" {
		token = c.Cookies(refreshCookieName)
	}

	user, tokens, err := h.svc.Refresh(c.UserContext(), token, h.requestMeta(c))
	if err != nil {
		clearRefreshCookie(c)
		return err
	}
	setRefreshCookie(c, tokens)
	return shared.OK(c, SessionResponse{
		User: user, Tokens: tokens, Permissions: PermissionsFor(user.Role),
	})
}

// Logout revokes the current session.
//
//	@Summary		Log out
//	@Tags			auth
//	@Produce		json
//	@Param			payload	body	refreshRequest	false	"Refresh token (omit when using the cookie)"
//	@Success		204
//	@Router			/auth/logout [post]
func (h *Handler) Logout(c *fiber.Ctx) error {
	var in refreshRequest
	_ = c.BodyParser(&in)
	token := in.RefreshToken
	if token == "" {
		token = c.Cookies(refreshCookieName)
	}
	if err := h.svc.Logout(c.UserContext(), token); err != nil {
		return err
	}
	clearRefreshCookie(c)
	return shared.NoContent(c)
}

// LogoutAll revokes every session belonging to the caller.
//
//	@Summary		Log out of every device
//	@Tags			auth
//	@Security		BearerAuth
//	@Success		204
//	@Router			/auth/logout-all [post]
func (h *Handler) LogoutAll(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	if err := h.svc.LogoutAll(c.UserContext(), id.UserID); err != nil {
		return err
	}
	clearRefreshCookie(c)
	return shared.NoContent(c)
}

// Me returns the authenticated user and their permissions.
//
//	@Summary		Current user
//	@Tags			auth
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	shared.Envelope{data=MeResponse}
//	@Failure		401	{object}	shared.ErrorEnvelope
//	@Router			/auth/me [get]
func (h *Handler) Me(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	user, err := h.svc.Me(c.UserContext(), id)
	if err != nil {
		return err
	}
	return shared.OK(c, MeResponse{User: user, Permissions: PermissionsFor(user.Role)})
}

type forgotPasswordRequest struct {
	Email string `json:"email" validate:"required,email"`
}

// ForgotPassword starts the password reset flow.
//
//	@Summary		Request a password reset
//	@Description	Always returns 202, whether or not the email is registered.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			payload	body	forgotPasswordRequest	true	"Email"
//	@Success		202
//	@Router			/auth/forgot-password [post]
func (h *Handler) ForgotPassword(c *fiber.Ctx) error {
	var in forgotPasswordRequest
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	if err := h.svc.RequestPasswordReset(c.UserContext(), in.Email, h.requestMeta(c)); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusAccepted)
}

// ResetPassword completes the password reset flow.
//
//	@Summary		Reset a password with a token
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			payload	body	ResetPasswordInput	true	"Reset token and new password"
//	@Success		204
//	@Failure		401	{object}	shared.ErrorEnvelope
//	@Router			/auth/reset-password [post]
func (h *Handler) ResetPassword(c *fiber.Ctx) error {
	var in ResetPasswordInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	if err := h.svc.ResetPassword(c.UserContext(), in, h.requestMeta(c)); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// ChangePassword updates the caller's own password.
//
//	@Summary		Change password
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			payload	body	ChangePasswordInput	true	"Current and new password"
//	@Success		204
//	@Failure		401	{object}	shared.ErrorEnvelope
//	@Router			/auth/change-password [post]
func (h *Handler) ChangePassword(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	var in ChangePasswordInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	if err := h.svc.ChangePassword(c.UserContext(), id, in); err != nil {
		return err
	}
	clearRefreshCookie(c)
	return shared.NoContent(c)
}

// ListUsers returns a page of organization users.
//
//	@Summary		List users
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Param			page		query		int		false	"Page number"
//	@Param			per_page	query		int		false	"Items per page"
//	@Param			q			query		string	false	"Search name or email"
//	@Param			sort		query		string	false	"Sort, e.g. -created_at"
//	@Success		200			{object}	shared.Envelope{data=[]User,meta=shared.PageMeta}
//	@Router			/users [get]
func (h *Handler) ListUsers(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	users, meta, err := h.svc.ListUsers(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, users, meta)
}

// CreateUser adds a user to the organization.
//
//	@Summary		Create a user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			payload	body		CreateUserInput	true	"User details"
//	@Success		201		{object}	shared.Envelope{data=User}
//	@Failure		409		{object}	shared.ErrorEnvelope
//	@Router			/users [post]
func (h *Handler) CreateUser(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	var in CreateUserInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	user, err := h.svc.CreateUser(c.UserContext(), id, in, h.requestMeta(c))
	if err != nil {
		return err
	}
	return shared.Created(c, user)
}

// UpdateUser edits a user.
//
//	@Summary		Update a user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string			true	"User id"
//	@Param			payload	body		UpdateUserInput	true	"Fields to change"
//	@Success		200		{object}	shared.Envelope{data=User}
//	@Failure		404		{object}	shared.ErrorEnvelope
//	@Router			/users/{id} [patch]
func (h *Handler) UpdateUser(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	userID, err := ParseID(c, "id")
	if err != nil {
		return err
	}
	var in UpdateUserInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	user, err := h.svc.UpdateUser(c.UserContext(), id, userID, in, h.requestMeta(c))
	if err != nil {
		return err
	}
	return shared.OK(c, user)
}

// DeleteUser soft-deletes a user.
//
//	@Summary		Delete a user
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path	string	true	"User id"
//	@Success		204
//	@Failure		404	{object}	shared.ErrorEnvelope
//	@Router			/users/{id} [delete]
func (h *Handler) DeleteUser(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	userID, err := ParseID(c, "id")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteUser(c.UserContext(), id, userID, h.requestMeta(c)); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// ListLoginAudits returns the authentication history.
//
//	@Summary		List login attempts
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	shared.Envelope{data=[]LoginAudit,meta=shared.PageMeta}
//	@Router			/users/login-audits [get]
func (h *Handler) ListLoginAudits(c *fiber.Ctx) error {
	id, err := FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListLoginAudits(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// ParseID reads a UUID path parameter, returning a 400 rather than a 500 when
// the client sends something that is not an id.
func ParseID(c *fiber.Ctx, name string) (uuid.UUID, error) {
	raw := c.Params(name)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, shared.Validation("invalid " + name).
			WithDetails(map[string]any{name: "must be a valid id"})
	}
	return id, nil
}

// --- refresh cookie --------------------------------------------------------

const refreshCookieName = "ims_refresh_token"

// setRefreshCookie stores the refresh token in an httpOnly, SameSite=Strict
// cookie. The web client uses the cookie (so the token is unreachable from JS,
// which blunts XSS); the mobile client ignores it and keeps the token in the
// OS keychain instead.
func setRefreshCookie(c *fiber.Ctx, tokens *TokenPair) {
	if tokens == nil {
		return
	}
	c.Cookie(&fiber.Cookie{
		Name:     refreshCookieName,
		Value:    tokens.RefreshToken,
		Path:     "/api/v1/auth",
		HTTPOnly: true,
		Secure:   c.Protocol() == "https",
		SameSite: fiber.CookieSameSiteStrictMode,
		MaxAge:   int((30 * 24 * 60 * 60)),
	})
}

func clearRefreshCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     "/api/v1/auth",
		HTTPOnly: true,
		MaxAge:   -1,
	})
}
