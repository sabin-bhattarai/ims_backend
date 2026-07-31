// Package notification delivers alerts over three channels: in-app rows, email
// and mobile push. It also owns the scheduled low-stock and expiry scans.
package notification

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
	"github.com/sabin-bhattarai/ims_backend/internal/platform"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
	"github.com/sabin-bhattarai/ims_backend/pkg/money"
)

// Type mirrors the notification_type enum.
type Type string

const (
	TypeLowStock          Type = "low_stock"
	TypeExpiryWarning     Type = "expiry_warning"
	TypePOPendingApproval Type = "po_pending_approval"
	TypePOApproved        Type = "po_approved"
	TypePORejected        Type = "po_rejected"
	TypeTransferReceived  Type = "transfer_received"
	TypeReturnRequested   Type = "return_requested"
	TypeReportReady       Type = "report_ready"
)

// Notification is an in-app alert.
type Notification struct {
	ID             uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID      `gorm:"type:uuid;not null" json:"organization_id"`
	UserID         *uuid.UUID     `gorm:"type:uuid" json:"user_id,omitempty"`
	Type           Type           `gorm:"type:notification_type;not null" json:"type"`
	Title          string         `gorm:"not null" json:"title"`
	Body           string         `gorm:"not null" json:"body"`
	Data           datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"data"`
	EntityType     *string        `json:"entity_type,omitempty"`
	EntityID       *uuid.UUID     `gorm:"type:uuid" json:"entity_id,omitempty"`
	ReadAt         *time.Time     `json:"read_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

func (Notification) TableName() string { return "notifications" }

// DeviceToken is a registered FCM token for push delivery.
type DeviceToken struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null" json:"organization_id"`
	UserID         uuid.UUID `gorm:"type:uuid;not null" json:"user_id"`
	Token          string    `gorm:"not null" json:"token"`
	Platform       string    `gorm:"not null" json:"platform"`
	DeviceName     *string   `json:"device_name,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (DeviceToken) TableName() string { return "device_tokens" }

// Service creates and delivers notifications.
type Service struct {
	db    *gorm.DB
	queue *platform.Queue
	cfg   platform.Config
	lg    zerolog.Logger
}

// NewService builds the notification service.
func NewService(db *gorm.DB, queue *platform.Queue, cfg platform.Config, lg zerolog.Logger) *Service {
	return &Service{db: db, queue: queue, cfg: cfg, lg: lg.With().Str("module", "notification").Logger()}
}

// Event describes a notification to raise.
type Event struct {
	OrgID      uuid.UUID
	Type       Type
	Title      string
	Body       string
	Data       map[string]any
	EntityType string
	EntityID   *uuid.UUID
	// Roles that should receive it; empty means every user in the organization.
	Roles []auth.Role
	// Email and Push request out-of-app delivery in addition to the in-app row.
	Email bool
	Push  bool
}

// Raise records an in-app notification and queues any email/push fan-out.
//
// Delivery never blocks the caller: a stock adjustment must not fail because
// SMTP is slow, so email and push go through the job queue and this method only
// writes the durable in-app row.
func (s *Service) Raise(ctx context.Context, e Event) {
	n := Notification{
		ID:             uuid.New(),
		OrganizationID: e.OrgID,
		Type:           e.Type,
		Title:          e.Title,
		Body:           e.Body,
		Data:           shared.JSONB(e.Data),
		EntityID:       e.EntityID,
	}
	if e.EntityType != "" {
		n.EntityType = &e.EntityType
	}

	// The partial unique index on open alerts collapses repeats: a product that
	// stays below its minimum produces one unread alert, not one per scan.
	err := s.db.WithContext(ctx).Create(&n).Error
	if err != nil {
		if isUniqueViolation(err) {
			s.lg.Debug().Str("type", string(e.Type)).Msg("open alert already exists; suppressed")
			return
		}
		s.lg.Error().Err(err).Str("type", string(e.Type)).Msg("failed to record notification")
		return
	}

	if !e.Email && !e.Push {
		return
	}

	recipients, err := s.recipients(ctx, e)
	if err != nil {
		s.lg.Error().Err(err).Msg("failed to resolve notification recipients")
		return
	}
	for _, r := range recipients {
		if e.Email && r.Email != "" {
			s.queue.Enqueue(ctx, platform.TaskSendEmail, EmailPayload{
				To: r.Email, Subject: e.Title, Body: e.Body, Name: r.FullName,
			})
		}
		if e.Push {
			s.queue.Enqueue(ctx, platform.TaskPushNotification, PushPayload{
				UserID: r.ID, Title: e.Title, Body: e.Body, Data: e.Data,
			})
		}
	}
}

type recipient struct {
	ID       uuid.UUID
	Email    string
	FullName string
}

func (s *Service) recipients(ctx context.Context, e Event) ([]recipient, error) {
	q := s.db.WithContext(ctx).Table("users").
		Select("id, email, full_name").
		Where("organization_id = ? AND status = 'active' AND deleted_at IS NULL", e.OrgID)
	if len(e.Roles) > 0 {
		q = q.Where("role IN ?", e.Roles)
	}
	var rows []recipient
	err := q.Scan(&rows).Error
	return rows, err
}

// LowStock implements stock.Alerter.
func (s *Service) LowStock(ctx context.Context, orgID, productID, warehouseID uuid.UUID, onHand, minStock money.Decimal) {
	var p struct {
		Name string
		SKU  string
	}
	if err := s.db.WithContext(ctx).Table("products").Select("name, sku").
		Where("id = ?", productID).Scan(&p).Error; err != nil {
		s.lg.Error().Err(err).Msg("low stock alert: product lookup failed")
		return
	}

	s.Raise(ctx, Event{
		OrgID: orgID,
		Type:  TypeLowStock,
		Title: "Low stock: " + p.Name,
		Body: p.Name + " (" + p.SKU + ") is down to " + onHand.String() +
			", at or below its minimum of " + minStock.String() + ".",
		Data: map[string]any{
			"product_id": productID, "warehouse_id": warehouseID,
			"on_hand": onHand, "min_stock": minStock,
		},
		EntityType: "product",
		EntityID:   &productID,
		Roles:      []auth.Role{auth.RoleAdmin, auth.RoleManager},
		Email:      true,
		Push:       true,
	})
}

// SendPasswordReset implements auth.Notifier.
func (s *Service) SendPasswordReset(ctx context.Context, to, fullName, resetURL string) {
	s.queue.Enqueue(ctx, platform.TaskSendEmail, EmailPayload{
		To:      to,
		Name:    fullName,
		Subject: "Reset your IMS password",
		Body: "Hello " + fullName + ",\n\nUse the link below to set a new password. " +
			"It expires in " + s.cfg.PasswordResetTTL.String() + ".\n\n" + resetURL +
			"\n\nIf you did not request this, you can ignore this email.",
	})
}

// SendWelcome implements auth.Notifier.
func (s *Service) SendWelcome(ctx context.Context, to, fullName string) {
	s.queue.Enqueue(ctx, platform.TaskSendEmail, EmailPayload{
		To:      to,
		Name:    fullName,
		Subject: "Welcome to IMS",
		Body:    "Hello " + fullName + ",\n\nYour inventory management account is ready.\n\n" + s.cfg.WebAppBaseURL,
	})
}

// List returns the caller's notifications: their own plus organization-wide.
func (s *Service) List(ctx context.Context, actor auth.Identity, q shared.Query) ([]Notification, shared.PageMeta, error) {
	tx := s.db.WithContext(ctx).Model(&Notification{}).
		Where("organization_id = ? AND (user_id = ? OR user_id IS NULL)", actor.OrgID, actor.UserID)

	if q.Filters["unread"] == "true" {
		tx = tx.Where("read_at IS NULL")
	}
	if v := q.Filters["type"]; v != "" {
		tx = tx.Where("type = ?", v)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var rows []Notification
	err := tx.Order("created_at DESC").Scopes(q.Paginate()).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// MarkRead marks one notification as read.
func (s *Service) MarkRead(ctx context.Context, actor auth.Identity, id uuid.UUID) error {
	res := s.db.WithContext(ctx).Model(&Notification{}).
		Where("id = ? AND organization_id = ? AND (user_id = ? OR user_id IS NULL)",
			id, actor.OrgID, actor.UserID).
		Update("read_at", time.Now().UTC())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return shared.NotFound("notification")
	}
	return nil
}

// MarkAllRead clears the caller's unread notifications.
func (s *Service) MarkAllRead(ctx context.Context, actor auth.Identity) error {
	return s.db.WithContext(ctx).Model(&Notification{}).
		Where("organization_id = ? AND (user_id = ? OR user_id IS NULL) AND read_at IS NULL",
			actor.OrgID, actor.UserID).
		Update("read_at", time.Now().UTC()).Error
}

// RegisterDeviceInput registers an FCM token.
type RegisterDeviceInput struct {
	Token      string `json:"token"       validate:"required,max=512"`
	Platform   string `json:"platform"    validate:"required,oneof=android ios web"`
	DeviceName string `json:"device_name" validate:"omitempty,max=120"`
}

// RegisterDevice stores or refreshes a push token for the caller.
func (s *Service) RegisterDevice(ctx context.Context, actor auth.Identity, in RegisterDeviceInput) (*DeviceToken, error) {
	var existing DeviceToken
	err := s.db.WithContext(ctx).Where("token = ?", in.Token).First(&existing).Error
	if err == nil {
		// Tokens migrate between users when a device is handed over, so
		// re-point rather than duplicate.
		updates := map[string]any{
			"user_id": actor.UserID, "organization_id": actor.OrgID,
			"platform": in.Platform, "last_seen_at": time.Now().UTC(),
		}
		if in.DeviceName != "" {
			updates["device_name"] = in.DeviceName
		}
		if err := s.db.WithContext(ctx).Model(&DeviceToken{}).
			Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	row := DeviceToken{
		ID: uuid.New(), OrganizationID: actor.OrgID, UserID: actor.UserID,
		Token: in.Token, Platform: in.Platform, LastSeenAt: time.Now().UTC(),
	}
	if in.DeviceName != "" {
		row.DeviceName = &in.DeviceName
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// UnregisterDevice removes a push token, called on sign-out.
func (s *Service) UnregisterDevice(ctx context.Context, actor auth.Identity, token string) error {
	return s.db.WithContext(ctx).
		Where("token = ? AND user_id = ?", token, actor.UserID).
		Delete(&DeviceToken{}).Error
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505") || strings.Contains(msg, "duplicate key value")
}

// Handler exposes the notification HTTP endpoints.
type Handler struct{ svc *Service }

// NewHandler builds the notification handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// List returns the caller's notifications.
//
//	@Summary	List notifications
//	@Tags		notifications
//	@Produce	json
//	@Security	BearerAuth
//	@Param		filter[unread]	query		bool	false	"Only unread"
//	@Success	200				{object}	shared.Envelope{data=[]Notification,meta=shared.PageMeta}
//	@Router		/notifications [get]
func (h *Handler) List(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.List(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// MarkRead marks one notification read.
//
//	@Summary	Mark a notification read
//	@Tags		notifications
//	@Security	BearerAuth
//	@Param		id	path	string	true	"Notification id"
//	@Success	204
//	@Router		/notifications/{id}/read [post]
func (h *Handler) MarkRead(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	nID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	if err := h.svc.MarkRead(c.UserContext(), id, nID); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// MarkAllRead clears every unread notification for the caller.
//
//	@Summary	Mark all notifications read
//	@Tags		notifications
//	@Security	BearerAuth
//	@Success	204
//	@Router		/notifications/read-all [post]
func (h *Handler) MarkAllRead(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	if err := h.svc.MarkAllRead(c.UserContext(), id); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// RegisterDevice registers a push token.
//
//	@Summary	Register a push device
//	@Tags		notifications
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		RegisterDeviceInput	true	"Device token"
//	@Success	201		{object}	shared.Envelope{data=DeviceToken}
//	@Router		/notifications/devices [post]
func (h *Handler) RegisterDevice(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in RegisterDeviceInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	row, err := h.svc.RegisterDevice(c.UserContext(), id, in)
	if err != nil {
		return err
	}
	return shared.Created(c, row)
}

// UnregisterDevice removes a push token.
//
//	@Summary	Unregister a push device
//	@Tags		notifications
//	@Security	BearerAuth
//	@Param		token	path	string	true	"Device token"
//	@Success	204
//	@Router		/notifications/devices/{token} [delete]
func (h *Handler) UnregisterDevice(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	if err := h.svc.UnregisterDevice(c.UserContext(), id, c.Params("token")); err != nil {
		return err
	}
	return shared.NoContent(c)
}
