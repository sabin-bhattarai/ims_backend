package shared

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sequencer allocates human-readable document codes such as PO-2026-000042.
//
// Codes are per organization, per document type and per year. Allocation takes
// a row lock on the counter, so two concurrent requests cannot mint the same
// code — a UUID would be unique but useless to a warehouse worker reading a
// printed pick list.
type Sequencer struct{}

// NewSequencer builds a Sequencer.
func NewSequencer() *Sequencer { return &Sequencer{} }

type documentSequence struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey"`
	DocType        string    `gorm:"primaryKey"`
	Period         string    `gorm:"primaryKey"`
	LastValue      int64
	UpdatedAt      time.Time
}

func (documentSequence) TableName() string { return "document_sequences" }

// Next allocates the next code for a document type. It must be called inside
// the same transaction as the document insert, so a rolled-back document does
// not burn a number.
func (s *Sequencer) Next(ctx context.Context, tx *gorm.DB, orgID uuid.UUID, docType, prefix string) (string, error) {
	period := time.Now().UTC().Format("2006")

	row := documentSequence{OrganizationID: orgID, DocType: docType, Period: period}
	// Insert-or-ignore so the first allocation of a period does not race.
	if err := tx.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row).Error; err != nil {
		return "", fmt.Errorf("ensure sequence row: %w", err)
	}

	var locked documentSequence
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("organization_id = ? AND doc_type = ? AND period = ?", orgID, docType, period).
		First(&locked).Error; err != nil {
		return "", fmt.Errorf("lock sequence: %w", err)
	}

	next := locked.LastValue + 1
	if err := tx.WithContext(ctx).Model(&documentSequence{}).
		Where("organization_id = ? AND doc_type = ? AND period = ?", orgID, docType, period).
		Updates(map[string]any{"last_value": next, "updated_at": time.Now().UTC()}).Error; err != nil {
		return "", fmt.Errorf("increment sequence: %w", err)
	}

	return fmt.Sprintf("%s-%s-%06d", prefix, period, next), nil
}
