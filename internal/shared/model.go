package shared

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Base is embedded by every persisted entity. UUID primary keys keep ids
// non-guessable and safe to generate client-side during offline mobile work.
type Base struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate assigns an id when the caller did not supply one.
func (b *Base) BeforeCreate(*gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

// OrgScoped is embedded by every tenant-owned entity. The system launches
// single-tenant but every query is org-scoped from day one, so enabling true
// multi-tenancy is a routing concern rather than a schema migration.
type OrgScoped struct {
	Base
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index" json:"organization_id"`
}

// InOrg returns a GORM scope restricting a query to a single organization.
// Every repository read and write goes through this.
func InOrg(orgID uuid.UUID) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("organization_id = ?", orgID)
	}
}
