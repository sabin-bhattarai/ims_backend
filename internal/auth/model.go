package auth

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"github.com/sabin-bhattarai/ims-backend/internal/shared"
)

// Organization is the tenant boundary.
type Organization struct {
	shared.Base
	Name     string         `gorm:"not null" json:"name"`
	Slug     string         `gorm:"not null" json:"slug"`
	Currency string         `gorm:"type:char(3);not null;default:USD" json:"currency"`
	Timezone string         `gorm:"not null;default:UTC" json:"timezone"`
	Settings datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"settings,omitempty"`
}

func (Organization) TableName() string { return "organizations" }

// UserStatus mirrors the user_status enum.
type UserStatus string

const (
	StatusActive    UserStatus = "active"
	StatusInvited   UserStatus = "invited"
	StatusSuspended UserStatus = "suspended"
)

// User is an operator account.
type User struct {
	shared.OrgScoped
	Email        string     `gorm:"type:citext;not null" json:"email"`
	FullName     string     `gorm:"not null" json:"full_name"`
	PasswordHash string     `gorm:"not null" json:"-"`
	Role         Role       `gorm:"type:user_role;not null;default:viewer" json:"role"`
	Status       UserStatus `gorm:"type:user_status;not null;default:active" json:"status"`
	Phone        *string    `json:"phone,omitempty"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

func (User) TableName() string { return "users" }

// CanLogin reports whether the account is permitted to authenticate.
func (u User) CanLogin() bool { return u.Status == StatusActive }

// RefreshToken is a stored session. Only the SHA-256 hash of the token is
// persisted.
type RefreshToken struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID     uuid.UUID  `gorm:"type:uuid;not null" json:"user_id"`
	TokenHash  string     `gorm:"not null" json:"-"`
	UserAgent  *string    `json:"user_agent,omitempty"`
	IPAddress  *string    `gorm:"type:inet" json:"ip_address,omitempty"`
	ExpiresAt  time.Time  `gorm:"not null" json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	ReplacedBy *uuid.UUID `gorm:"type:uuid" json:"replaced_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (RefreshToken) TableName() string { return "refresh_tokens" }

// Active reports whether the session is still usable.
func (t RefreshToken) Active() bool {
	return t.RevokedAt == nil && time.Now().UTC().Before(t.ExpiresAt)
}

// PasswordReset is a single-use password reset grant.
type PasswordReset struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null" json:"user_id"`
	TokenHash string     `gorm:"not null" json:"-"`
	ExpiresAt time.Time  `gorm:"not null" json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func (PasswordReset) TableName() string { return "password_resets" }

// LoginAudit records an authentication attempt, successful or not.
type LoginAudit struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    *uuid.UUID `gorm:"type:uuid" json:"user_id,omitempty"`
	Email     string     `gorm:"type:citext;not null" json:"email"`
	Success   bool       `gorm:"not null" json:"success"`
	Reason    *string    `json:"reason,omitempty"`
	IPAddress *string    `gorm:"type:inet" json:"ip_address,omitempty"`
	UserAgent *string    `json:"user_agent,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func (LoginAudit) TableName() string { return "login_audits" }
