package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims_backend/internal/platform"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// Notifier sends account emails. The notification module implements it; auth
// depends on the interface so the two modules stay decoupled and the service
// is testable without SMTP.
type Notifier interface {
	SendPasswordReset(ctx context.Context, to, fullName, resetURL string)
	SendWelcome(ctx context.Context, to, fullName string)
}

// Service holds the authentication and user-management business logic.
type Service struct {
	db       *gorm.DB
	cfg      platform.Config
	tokens   *TokenIssuer
	audit    *shared.Auditor
	notifier Notifier
	lg       zerolog.Logger
}

// NewService builds the auth service.
func NewService(
	db *gorm.DB,
	cfg platform.Config,
	tokens *TokenIssuer,
	audit *shared.Auditor,
	notifier Notifier,
	lg zerolog.Logger,
) *Service {
	return &Service{
		db: db, cfg: cfg, tokens: tokens, audit: audit, notifier: notifier,
		lg: lg.With().Str("module", "auth").Logger(),
	}
}

// RegisterInput creates a brand-new organization and its first admin user.
type RegisterInput struct {
	OrganizationName string `json:"organization_name" validate:"required,min=2,max=120"`
	FullName         string `json:"full_name"         validate:"required,min=2,max=120"`
	Email            string `json:"email"             validate:"required,email,max=255"`
	Password         string `json:"password"          validate:"required,min=10,max=128"`
}

// Register provisions an organization with a default warehouse-less setup and
// an admin user. The first user of an organization is always an admin;
// subsequent users are invited by that admin with an explicit role.
func (s *Service) Register(ctx context.Context, in RegisterInput, meta RequestMeta) (*User, *TokenPair, error) {
	email := normalizeEmail(in.Email)

	var (
		user User
		pair *TokenPair
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&User{}).Where("email = ?", email).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return shared.Conflict("an account with this email already exists")
		}

		org := Organization{
			Name: strings.TrimSpace(in.OrganizationName),
			Slug: slugify(in.OrganizationName),
		}
		if err := tx.Create(&org).Error; err != nil {
			return fmt.Errorf("create organization: %w", err)
		}

		hash, err := s.hashPassword(in.Password)
		if err != nil {
			return err
		}

		user = User{
			Email:        email,
			FullName:     strings.TrimSpace(in.FullName),
			PasswordHash: hash,
			Role:         RoleAdmin,
			Status:       StatusActive,
		}
		user.OrganizationID = org.ID
		if err := tx.Create(&user).Error; err != nil {
			return fmt.Errorf("create user: %w", err)
		}

		pair, err = s.issueSession(ctx, tx, user, meta)
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	s.notifier.SendWelcome(ctx, user.Email, user.FullName)
	s.audit.Record(ctx, shared.Actor{ID: user.ID, Email: user.Email, OrgID: user.OrganizationID, IP: meta.IP, RequestID: meta.RequestID},
		shared.AuditEntry{Action: "organization.register", EntityType: "organization", EntityID: &user.OrganizationID})

	return &user, pair, nil
}

// LoginInput is a credential submission.
type LoginInput struct {
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// RequestMeta carries the client fingerprint recorded against sessions and
// audit rows.
type RequestMeta struct {
	IP        string
	UserAgent string
	RequestID string
}

// Login verifies credentials and opens a session.
//
// Every outcome — including an unknown email — is written to login_audits, and
// every failure returns the same generic message, so the endpoint cannot be
// used to enumerate valid accounts.
func (s *Service) Login(ctx context.Context, in LoginInput, meta RequestMeta) (*User, *TokenPair, error) {
	email := normalizeEmail(in.Email)
	genericErr := shared.Unauthorized("invalid email or password")

	var user User
	err := s.db.WithContext(ctx).Where("email = ?", email).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Hash anyway: returning early on an unknown email makes response time
		// a user-enumeration oracle.
		_, _ = bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost())
		s.recordLogin(ctx, nil, email, false, "unknown_email", meta)
		return nil, nil, genericErr
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lookup user: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.Password)) != nil {
		s.recordLogin(ctx, &user.ID, email, false, "bad_password", meta)
		return nil, nil, genericErr
	}
	if !user.CanLogin() {
		s.recordLogin(ctx, &user.ID, email, false, "status_"+string(user.Status), meta)
		return nil, nil, shared.Forbidden("this account is " + string(user.Status))
	}

	var pair *TokenPair
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Model(&User{}).Where("id = ?", user.ID).
			Update("last_login_at", now).Error; err != nil {
			return err
		}
		user.LastLoginAt = &now

		var err error
		pair, err = s.issueSession(ctx, tx, user, meta)
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	s.recordLogin(ctx, &user.ID, email, true, "", meta)
	return &user, pair, nil
}

// Refresh rotates a refresh token and issues a new access token.
//
// Rotation is single-use: the presented token is revoked and linked to its
// replacement. If a token that was already rotated is presented again, the
// whole session family is revoked — that pattern means the token leaked.
func (s *Service) Refresh(ctx context.Context, refreshToken string, meta RequestMeta) (*User, *TokenPair, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, nil, shared.Unauthorized("refresh token is required")
	}
	hash := HashToken(refreshToken)

	var (
		user User
		pair *TokenPair
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored RefreshToken
		err := tx.Where("token_hash = ?", hash).First(&stored).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return shared.Unauthorized("invalid refresh token")
		}
		if err != nil {
			return err
		}

		if stored.RevokedAt != nil {
			// Replay of a rotated token: assume compromise and end every
			// session for this user.
			s.lg.Warn().Str("user_id", stored.UserID.String()).
				Msg("refresh token replay detected; revoking all sessions")
			if err := tx.Model(&RefreshToken{}).
				Where("user_id = ? AND revoked_at IS NULL", stored.UserID).
				Update("revoked_at", time.Now().UTC()).Error; err != nil {
				return err
			}
			return shared.Unauthorized("session revoked; please sign in again")
		}
		if !stored.Active() {
			return shared.Unauthorized("refresh token expired")
		}

		if err := tx.Where("id = ?", stored.UserID).First(&user).Error; err != nil {
			return shared.Unauthorized("account no longer exists")
		}
		if !user.CanLogin() {
			return shared.Forbidden("this account is " + string(user.Status))
		}

		newPair, newID, err := s.mintSession(ctx, tx, user, meta)
		if err != nil {
			return err
		}
		pair = newPair

		return tx.Model(&RefreshToken{}).Where("id = ?", stored.ID).Updates(map[string]any{
			"revoked_at":  time.Now().UTC(),
			"replaced_by": newID,
		}).Error
	})
	if err != nil {
		return nil, nil, err
	}
	return &user, pair, nil
}

// Logout revokes the presented refresh token. It is idempotent: signing out
// twice, or with an already-expired token, is not an error.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if strings.TrimSpace(refreshToken) == "" {
		return nil
	}
	return s.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("token_hash = ? AND revoked_at IS NULL", HashToken(refreshToken)).
		Update("revoked_at", time.Now().UTC()).Error
}

// LogoutAll revokes every session for a user, used by "sign out everywhere"
// and after a password change.
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	return s.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", time.Now().UTC()).Error
}

// RequestPasswordReset issues a reset token and emails it.
//
// It always reports success, whether or not the email exists, for the same
// enumeration reason as Login.
func (s *Service) RequestPasswordReset(ctx context.Context, email string, meta RequestMeta) error {
	email = normalizeEmail(email)

	var user User
	err := s.db.WithContext(ctx).Where("email = ?", email).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s.lg.Info().Str("email", email).Msg("password reset requested for unknown email")
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}

	plaintext, hash, err := NewOpaqueToken()
	if err != nil {
		return err
	}
	reset := PasswordReset{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: hash,
		ExpiresAt: time.Now().UTC().Add(s.cfg.PasswordResetTTL),
	}
	if err := s.db.WithContext(ctx).Create(&reset).Error; err != nil {
		return fmt.Errorf("create password reset: %w", err)
	}

	resetURL := fmt.Sprintf("%s/reset-password?token=%s", strings.TrimRight(s.cfg.WebAppBaseURL, "/"), plaintext)
	s.notifier.SendPasswordReset(ctx, user.Email, user.FullName, resetURL)
	s.audit.Record(ctx, shared.Actor{ID: user.ID, Email: user.Email, OrgID: user.OrganizationID, IP: meta.IP, RequestID: meta.RequestID},
		shared.AuditEntry{Action: "user.password_reset_requested", EntityType: "user", EntityID: &user.ID})
	return nil
}

// ResetPasswordInput completes a reset.
type ResetPasswordInput struct {
	Token       string `json:"token"        validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=10,max=128"`
}

// ResetPassword consumes a reset token, sets the new password and revokes all
// existing sessions so a thief holding a stolen refresh token is evicted.
func (s *Service) ResetPassword(ctx context.Context, in ResetPasswordInput, meta RequestMeta) error {
	hash := HashToken(in.Token)

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reset PasswordReset
		err := tx.Where("token_hash = ?", hash).First(&reset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return shared.Unauthorized("invalid or expired reset token")
		}
		if err != nil {
			return err
		}
		if reset.UsedAt != nil || time.Now().UTC().After(reset.ExpiresAt) {
			return shared.Unauthorized("invalid or expired reset token")
		}

		newHash, err := s.hashPassword(in.NewPassword)
		if err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", reset.UserID).
			Update("password_hash", newHash).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Model(&PasswordReset{}).Where("id = ?", reset.ID).
			Update("used_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&RefreshToken{}).
			Where("user_id = ? AND revoked_at IS NULL", reset.UserID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}

		var user User
		if err := tx.Where("id = ?", reset.UserID).First(&user).Error; err == nil {
			return s.audit.RecordTx(ctx, tx,
				shared.Actor{ID: user.ID, Email: user.Email, OrgID: user.OrganizationID, IP: meta.IP, RequestID: meta.RequestID},
				shared.AuditEntry{Action: "user.password_reset", EntityType: "user", EntityID: &user.ID})
		}
		return nil
	})
}

// ChangePasswordInput is an authenticated password change.
type ChangePasswordInput struct {
	CurrentPassword string `json:"current_password" validate:"required"`
	NewPassword     string `json:"new_password"     validate:"required,min=10,max=128"`
}

// ChangePassword verifies the current password before replacing it.
func (s *Service) ChangePassword(ctx context.Context, id Identity, in ChangePasswordInput) error {
	var user User
	if err := s.db.WithContext(ctx).Where("id = ?", id.UserID).First(&user).Error; err != nil {
		return shared.NotFound("user")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.CurrentPassword)) != nil {
		return shared.Unauthorized("current password is incorrect")
	}
	if subtle.ConstantTimeCompare([]byte(in.CurrentPassword), []byte(in.NewPassword)) == 1 {
		return shared.Validation("new password must differ from the current one")
	}

	hash, err := s.hashPassword(in.NewPassword)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", user.ID).
		Update("password_hash", hash).Error; err != nil {
		return err
	}
	return s.LogoutAll(ctx, user.ID)
}

// Me returns the caller's user record.
func (s *Service) Me(ctx context.Context, id Identity) (*User, error) {
	var user User
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(id.OrgID)).
		Where("id = ?", id.UserID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("user")
	}
	return &user, err
}

// --- user management -------------------------------------------------------

// CreateUserInput invites a teammate.
type CreateUserInput struct {
	FullName string `json:"full_name" validate:"required,min=2,max=120"`
	Email    string `json:"email"     validate:"required,email,max=255"`
	Password string `json:"password"  validate:"required,min=10,max=128"`
	Role     Role   `json:"role"      validate:"required,oneof=admin manager warehouse_staff viewer"`
	Phone    string `json:"phone"     validate:"omitempty,max=32"`
}

// CreateUser adds a user to the caller's organization.
func (s *Service) CreateUser(ctx context.Context, actor Identity, in CreateUserInput, meta RequestMeta) (*User, error) {
	email := normalizeEmail(in.Email)

	hash, err := s.hashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	user := User{
		Email:        email,
		FullName:     strings.TrimSpace(in.FullName),
		PasswordHash: hash,
		Role:         in.Role,
		Status:       StatusActive,
	}
	user.OrganizationID = actor.OrgID
	if in.Phone != "" {
		user.Phone = &in.Phone
	}

	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("an account with this email already exists")
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	s.notifier.SendWelcome(ctx, user.Email, user.FullName)
	s.audit.Record(ctx, actorFrom(actor, meta),
		shared.AuditEntry{Action: "user.create", EntityType: "user", EntityID: &user.ID, After: user})
	return &user, nil
}

// UpdateUserInput edits a teammate. Fields left nil are unchanged.
type UpdateUserInput struct {
	FullName *string     `json:"full_name" validate:"omitempty,min=2,max=120"`
	Role     *Role       `json:"role"      validate:"omitempty,oneof=admin manager warehouse_staff viewer"`
	Status   *UserStatus `json:"status"    validate:"omitempty,oneof=active invited suspended"`
	Phone    *string     `json:"phone"     validate:"omitempty,max=32"`
}

// UpdateUser applies changes to a user in the caller's organization.
//
// Two self-inflicted lockouts are blocked here: demoting yourself out of admin
// and suspending your own account.
func (s *Service) UpdateUser(ctx context.Context, actor Identity, userID uuid.UUID, in UpdateUserInput, meta RequestMeta) (*User, error) {
	var user User
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Where("id = ?", userID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("user")
	}
	if err != nil {
		return nil, err
	}
	before := user

	updates := map[string]any{}
	if in.FullName != nil {
		updates["full_name"] = strings.TrimSpace(*in.FullName)
	}
	if in.Phone != nil {
		updates["phone"] = *in.Phone
	}
	if in.Role != nil && *in.Role != user.Role {
		if user.ID == actor.UserID {
			return nil, shared.Validation("you cannot change your own role")
		}
		if err := s.assertNotLastAdmin(ctx, actor.OrgID, user); err != nil {
			return nil, err
		}
		updates["role"] = *in.Role
	}
	if in.Status != nil && *in.Status != user.Status {
		if user.ID == actor.UserID {
			return nil, shared.Validation("you cannot change your own status")
		}
		if *in.Status != StatusActive {
			if err := s.assertNotLastAdmin(ctx, actor.OrgID, user); err != nil {
				return nil, err
			}
		}
		updates["status"] = *in.Status
	}
	if len(updates) == 0 {
		return &user, nil
	}

	if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", user.ID).
		Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	if err := s.db.WithContext(ctx).Where("id = ?", user.ID).First(&user).Error; err != nil {
		return nil, err
	}

	// A demoted or suspended user must lose their existing sessions, otherwise
	// their access token keeps its old role until it expires.
	if in.Role != nil || in.Status != nil {
		if err := s.LogoutAll(ctx, user.ID); err != nil {
			s.lg.Error().Err(err).Msg("failed to revoke sessions after role/status change")
		}
	}

	s.audit.Record(ctx, actorFrom(actor, meta), shared.AuditEntry{
		Action: "user.update", EntityType: "user", EntityID: &user.ID,
		Before: before, After: user,
	})
	return &user, nil
}

// DeleteUser soft-deletes a user and ends their sessions.
func (s *Service) DeleteUser(ctx context.Context, actor Identity, userID uuid.UUID, meta RequestMeta) error {
	if userID == actor.UserID {
		return shared.Validation("you cannot delete your own account")
	}
	var user User
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Where("id = ?", userID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return shared.NotFound("user")
	}
	if err != nil {
		return err
	}
	if err := s.assertNotLastAdmin(ctx, actor.OrgID, user); err != nil {
		return err
	}

	if err := s.db.WithContext(ctx).Delete(&user).Error; err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if err := s.LogoutAll(ctx, user.ID); err != nil {
		s.lg.Error().Err(err).Msg("failed to revoke sessions after user delete")
	}
	s.audit.Record(ctx, actorFrom(actor, meta),
		shared.AuditEntry{Action: "user.delete", EntityType: "user", EntityID: &user.ID, Before: user})
	return nil
}

// ListUsers returns a page of users in the caller's organization.
func (s *Service) ListUsers(ctx context.Context, actor Identity, q shared.Query) ([]User, shared.PageMeta, error) {
	sortable := map[string]string{
		"created_at": "created_at", "full_name": "full_name",
		"email": "email", "role": "role", "last_login_at": "last_login_at",
	}
	filterable := map[string]string{"role": "role", "status": "status"}

	tx := s.db.WithContext(ctx).Model(&User{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))
	if q.Search != "" {
		like := "%" + strings.ToLower(q.Search) + "%"
		tx = tx.Where("lower(full_name) LIKE ? OR lower(email::text) LIKE ?", like, like)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}

	var users []User
	err := tx.Scopes(q.OrderBy(sortable, "created_at DESC"), q.Paginate()).Find(&users).Error
	return users, shared.NewPageMeta(q, total), err
}

// ListLoginAudits returns the authentication history for the organization.
func (s *Service) ListLoginAudits(ctx context.Context, actor Identity, q shared.Query) ([]LoginAudit, shared.PageMeta, error) {
	// Scoped through users because login_audits also records attempts for
	// emails that belong to no account.
	tx := s.db.WithContext(ctx).Model(&LoginAudit{}).
		Where("user_id IN (SELECT id FROM users WHERE organization_id = ?)", actor.OrgID)

	if v, ok := q.Filters["success"]; ok && v != "" {
		tx = tx.Where("success = ?", v == "true")
	}
	if q.Search != "" {
		tx = tx.Where("email::text ILIKE ?", "%"+q.Search+"%")
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var rows []LoginAudit
	err := tx.Order("created_at DESC").Scopes(q.Paginate()).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// --- internals -------------------------------------------------------------

// issueSession mints a token pair for a fresh login.
func (s *Service) issueSession(ctx context.Context, tx *gorm.DB, u User, meta RequestMeta) (*TokenPair, error) {
	pair, _, err := s.mintSession(ctx, tx, u, meta)
	return pair, err
}

// mintSession creates the refresh-token row and the matching access token,
// returning the new session id so callers can chain rotations.
func (s *Service) mintSession(ctx context.Context, tx *gorm.DB, u User, meta RequestMeta) (*TokenPair, uuid.UUID, error) {
	plaintext, hash, err := NewOpaqueToken()
	if err != nil {
		return nil, uuid.Nil, err
	}

	row := RefreshToken{
		ID:        uuid.New(),
		UserID:    u.ID,
		TokenHash: hash,
		ExpiresAt: time.Now().UTC().Add(s.tokens.RefreshTTL()),
	}
	if meta.UserAgent != "" {
		row.UserAgent = &meta.UserAgent
	}
	if meta.IP != "" {
		row.IPAddress = &meta.IP
	}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, uuid.Nil, fmt.Errorf("create refresh token: %w", err)
	}

	access, exp, err := s.tokens.AccessToken(u, row.ID.String())
	if err != nil {
		return nil, uuid.Nil, err
	}

	return &TokenPair{
		AccessToken:  access,
		RefreshToken: plaintext,
		TokenType:    "Bearer",
		ExpiresIn:    int(time.Until(exp).Seconds()),
		ExpiresAt:    exp,
	}, row.ID, nil
}

// assertNotLastAdmin refuses a change that would leave an organization with no
// active admin, which would lock everyone out of user management.
func (s *Service) assertNotLastAdmin(ctx context.Context, orgID uuid.UUID, target User) error {
	if target.Role != RoleAdmin {
		return nil
	}
	var admins int64
	err := s.db.WithContext(ctx).Model(&User{}).
		Where("organization_id = ? AND role = ? AND status = ? AND id <> ?",
			orgID, RoleAdmin, StatusActive, target.ID).
		Count(&admins).Error
	if err != nil {
		return err
	}
	if admins == 0 {
		return shared.Validation("the organization must keep at least one active admin")
	}
	return nil
}

func (s *Service) recordLogin(ctx context.Context, userID *uuid.UUID, email string, success bool, reason string, meta RequestMeta) {
	row := LoginAudit{ID: uuid.New(), UserID: userID, Email: email, Success: success}
	if reason != "" {
		row.Reason = &reason
	}
	if meta.IP != "" {
		row.IPAddress = &meta.IP
	}
	if meta.UserAgent != "" {
		row.UserAgent = &meta.UserAgent
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		s.lg.Error().Err(err).Msg("failed to record login audit")
	}
}

func (s *Service) hashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), s.bcryptCost())
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func (s *Service) bcryptCost() int {
	if s.cfg.BcryptCost < bcrypt.MinCost || s.cfg.BcryptCost > bcrypt.MaxCost {
		return bcrypt.DefaultCost
	}
	return s.cfg.BcryptCost
}

func actorFrom(id Identity, meta RequestMeta) shared.Actor {
	return shared.Actor{
		ID: id.UserID, Email: id.Email, OrgID: id.OrgID,
		IP: meta.IP, RequestID: meta.RequestID,
	}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "org"
	}
	// Collisions are possible; append a short suffix to keep the slug unique.
	return slug + "-" + uuid.NewString()[:8]
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
