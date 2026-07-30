package notification

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

// This file holds the adapters that satisfy the Notifier interfaces declared by
// the purchasing and sales modules. Those modules depend on their own narrow
// interface rather than on this package, which keeps the dependency pointing
// one way and lets them be tested without a queue.

// POPendingApproval implements purchasing.Notifier. Only roles that can
// actually approve are notified — telling warehouse staff about a PO they
// cannot action is noise.
func (s *Service) POPendingApproval(ctx context.Context, orgID, poID uuid.UUID, code string, total money.Decimal) {
	s.Raise(ctx, Event{
		OrgID:      orgID,
		Type:       TypePOPendingApproval,
		Title:      "Purchase order " + code + " needs approval",
		Body:       fmt.Sprintf("%s has been submitted for approval with a total of %s.", code, total.String()),
		Data:       map[string]any{"purchase_order_id": poID, "code": code, "total": total},
		EntityType: "purchase_order",
		EntityID:   &poID,
		Roles:      []auth.Role{auth.RoleAdmin, auth.RoleManager},
		Email:      true,
		Push:       true,
	})
}

// PODecided implements purchasing.Notifier, telling the raiser the outcome.
func (s *Service) PODecided(ctx context.Context, orgID, poID uuid.UUID, code string, approved bool, requesterID *uuid.UUID, reason string) {
	notifType, title := TypePORejected, "Purchase order "+code+" was rejected"
	body := "Your purchase order " + code + " was rejected."
	if reason != "" {
		body += " Reason: " + reason
	}
	if approved {
		notifType, title = TypePOApproved, "Purchase order "+code+" was approved"
		body = "Your purchase order " + code + " has been approved and can now be received."
	}

	event := Event{
		OrgID: orgID, Type: notifType, Title: title, Body: body,
		Data:       map[string]any{"purchase_order_id": poID, "code": code, "approved": approved},
		EntityType: "purchase_order",
		EntityID:   &poID,
		Email:      true,
		Push:       true,
	}
	s.Raise(ctx, event)

	// The in-app row raised above is org-wide; also target the raiser directly
	// so it surfaces in their own list.
	if requesterID != nil {
		s.raiseForUser(ctx, *requesterID, event)
	}
}

// ReturnRequested implements sales.Notifier.
func (s *Service) ReturnRequested(ctx context.Context, orgID, returnID uuid.UUID, code, reason string) {
	s.Raise(ctx, Event{
		OrgID:      orgID,
		Type:       TypeReturnRequested,
		Title:      "Return " + code + " requested",
		Body:       "A return has been requested. Reason: " + reason,
		Data:       map[string]any{"return_id": returnID, "code": code},
		EntityType: "sales_return",
		EntityID:   &returnID,
		Roles:      []auth.Role{auth.RoleAdmin, auth.RoleManager},
		Email:      true,
	})
}

// raiseForUser writes a user-targeted copy of an event.
func (s *Service) raiseForUser(ctx context.Context, userID uuid.UUID, e Event) {
	n := Notification{
		ID:             uuid.New(),
		OrganizationID: e.OrgID,
		UserID:         &userID,
		Type:           e.Type,
		Title:          e.Title,
		Body:           e.Body,
		Data:           shared.JSONB(e.Data),
		EntityID:       e.EntityID,
	}
	if e.EntityType != "" {
		n.EntityType = &e.EntityType
	}
	if err := s.db.WithContext(ctx).Create(&n).Error; err != nil && !isUniqueViolation(err) {
		s.lg.Error().Err(err).Msg("failed to record user notification")
	}
}
