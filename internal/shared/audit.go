package shared

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AuditLog is one recorded change to a non-stock entity. Stock changes are
// additionally (and authoritatively) recorded in stock_movements.
type AuditLog struct {
	ID             uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID      `gorm:"type:uuid;not null" json:"organization_id"`
	ActorID        *uuid.UUID     `gorm:"type:uuid" json:"actor_id,omitempty"`
	ActorEmail     *string        `gorm:"type:citext" json:"actor_email,omitempty"`
	Action         string         `gorm:"not null" json:"action"`
	EntityType     string         `gorm:"not null" json:"entity_type"`
	EntityID       *uuid.UUID     `gorm:"type:uuid" json:"entity_id,omitempty"`
	Before         datatypes.JSON `gorm:"type:jsonb" json:"before,omitempty"`
	After          datatypes.JSON `gorm:"type:jsonb" json:"after,omitempty"`
	IPAddress      *string        `gorm:"type:inet" json:"ip_address,omitempty"`
	RequestID      *string        `json:"request_id,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }

// Actor identifies who performed an action. It is a plain struct rather than
// auth.Identity to keep this package free of module dependencies.
type Actor struct {
	ID        uuid.UUID
	Email     string
	OrgID     uuid.UUID
	IP        string
	RequestID string
}

// AuditEntry describes a single change to record.
type AuditEntry struct {
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	Before     any
	After      any
}

// Auditor writes audit entries.
type Auditor struct {
	db *gorm.DB
	lg zerolog.Logger
}

// NewAuditor builds an Auditor.
func NewAuditor(db *gorm.DB, lg zerolog.Logger) *Auditor {
	return &Auditor{db: db, lg: lg.With().Str("component", "audit").Logger()}
}

// Record writes an audit row. Failing to record is logged but never fails the
// caller's request: losing an audit line is bad, but rolling back a completed
// stock receipt because the audit insert failed is worse, and the stock ledger
// remains the authoritative record either way.
func (a *Auditor) Record(ctx context.Context, actor Actor, e AuditEntry) {
	if err := a.RecordTx(ctx, a.db, actor, e); err != nil {
		a.lg.Error().Err(err).
			Str("action", e.Action).
			Str("entity", e.EntityType).
			Msg("failed to write audit log")
	}
}

// RecordTx writes an audit row inside an existing transaction, so the entry
// commits or rolls back with the change it describes. Prefer this for
// stock-affecting work.
func (a *Auditor) RecordTx(ctx context.Context, tx *gorm.DB, actor Actor, e AuditEntry) error {
	row := AuditLog{
		ID:             uuid.New(),
		OrganizationID: actor.OrgID,
		Action:         e.Action,
		EntityType:     e.EntityType,
		EntityID:       e.EntityID,
		Before:         toJSON(e.Before),
		After:          toJSON(e.After),
		CreatedAt:      time.Now().UTC(),
	}
	if actor.ID != uuid.Nil {
		row.ActorID = &actor.ID
	}
	if actor.Email != "" {
		row.ActorEmail = &actor.Email
	}
	if actor.IP != "" {
		row.IPAddress = &actor.IP
	}
	if actor.RequestID != "" {
		row.RequestID = &actor.RequestID
	}
	return tx.WithContext(ctx).Create(&row).Error
}

// JSONB marshals a value for storage in a jsonb column, returning nil (SQL
// NULL) when the value cannot be encoded.
func JSONB(v any) datatypes.JSON { return toJSON(v) }

func toJSON(v any) datatypes.JSON {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return datatypes.JSON(b)
}
