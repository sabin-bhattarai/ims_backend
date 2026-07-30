package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/platform"
	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

// EmailPayload is the queued email job.
type EmailPayload struct {
	To      string `json:"to"`
	Name    string `json:"name"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// PushPayload is the queued push job.
type PushPayload struct {
	UserID uuid.UUID      `json:"user_id"`
	Title  string         `json:"title"`
	Body   string         `json:"body"`
	Data   map[string]any `json:"data,omitempty"`
}

// Worker executes queued notification jobs and the scheduled stock scans.
type Worker struct {
	db  *gorm.DB
	svc *Service
	cfg platform.Config
	lg  zerolog.Logger
	// http is reused across FCM calls; a fresh client per push leaks
	// connections under load.
	http *http.Client
}

// NewWorker builds the notification worker.
func NewWorker(db *gorm.DB, svc *Service, cfg platform.Config, lg zerolog.Logger) *Worker {
	return &Worker{
		db: db, svc: svc, cfg: cfg,
		lg:   lg.With().Str("component", "notification-worker").Logger(),
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Register wires the worker's handlers onto the Asynq mux.
func (w *Worker) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(platform.TaskSendEmail, w.handleEmail)
	mux.HandleFunc(platform.TaskPushNotification, w.handlePush)
	mux.HandleFunc(platform.TaskLowStockScan, w.handleLowStockScan)
	mux.HandleFunc(platform.TaskExpiryScan, w.handleExpiryScan)
}

func (w *Worker) handleEmail(ctx context.Context, t *asynq.Task) error {
	var p EmailPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		// A malformed payload will never succeed; SkipRetry stops Asynq from
		// retrying it five times.
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}
	if w.cfg.SMTPHost == "" {
		w.lg.Debug().Str("to", p.To).Msg("SMTP not configured; email skipped")
		return nil
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		w.cfg.SMTPFrom, p.To, p.Subject, p.Body)

	addr := fmt.Sprintf("%s:%d", w.cfg.SMTPHost, w.cfg.SMTPPort)
	var authMech smtp.Auth
	if w.cfg.SMTPUsername != "" {
		authMech = smtp.PlainAuth("", w.cfg.SMTPUsername, w.cfg.SMTPPassword, w.cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, authMech, w.cfg.SMTPFrom, []string{p.To}, []byte(msg)); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	w.lg.Info().Str("to", p.To).Str("subject", p.Subject).Msg("email sent")
	return nil
}

func (w *Worker) handlePush(ctx context.Context, t *asynq.Task) error {
	var p PushPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}
	if w.cfg.FCMServerKey == "" {
		w.lg.Debug().Msg("FCM not configured; push skipped")
		return nil
	}

	var tokens []string
	if err := w.db.WithContext(ctx).Table("device_tokens").
		Where("user_id = ?", p.UserID).Pluck("token", &tokens).Error; err != nil {
		return fmt.Errorf("load device tokens: %w", err)
	}
	if len(tokens) == 0 {
		return nil
	}

	for _, token := range tokens {
		body, err := json.Marshal(map[string]any{
			"to":           token,
			"notification": map[string]any{"title": p.Title, "body": p.Body},
			"data":         p.Data,
		})
		if err != nil {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://fcm.googleapis.com/fcm/send", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "key="+w.cfg.FCMServerKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := w.http.Do(req)
		if err != nil {
			return fmt.Errorf("fcm request: %w", err)
		}
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			// A stale token is permanent: delete it rather than retrying.
			w.lg.Info().Msg("removing rejected device token")
			w.db.WithContext(ctx).Where("token = ?", token).Delete(&DeviceToken{})
		case resp.StatusCode >= 500:
			return fmt.Errorf("fcm unavailable: status %d", resp.StatusCode)
		}
	}
	return nil
}

// handleLowStockScan sweeps every product below its minimum. Adjustments
// already alert inline; this catches thresholds that were raised after the fact
// and anything missed while the queue was down.
func (w *Worker) handleLowStockScan(ctx context.Context, _ *asynq.Task) error {
	type row struct {
		OrganizationID uuid.UUID
		ProductID      uuid.UUID
		WarehouseID    uuid.UUID
		OnHand         money.Decimal
		MinStock       money.Decimal
	}

	var rows []row
	err := w.db.WithContext(ctx).
		Table("stock_items si").
		Select(`si.organization_id, si.product_id, si.warehouse_id,
		        SUM(si.quantity) AS on_hand, p.min_stock`).
		Joins("JOIN products p ON p.id = si.product_id AND p.deleted_at IS NULL AND p.is_active").
		Where("p.min_stock > 0").
		Group("si.organization_id, si.product_id, si.warehouse_id, p.min_stock").
		Having("SUM(si.quantity) <= p.min_stock").
		Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("low stock scan: %w", err)
	}

	for _, r := range rows {
		w.svc.LowStock(ctx, r.OrganizationID, r.ProductID, r.WarehouseID, r.OnHand, r.MinStock)
	}
	w.lg.Info().Int("alerts", len(rows)).Msg("low stock scan complete")
	return nil
}

// handleExpiryScan warns about batches expiring within 30 days that still hold
// stock.
func (w *Worker) handleExpiryScan(ctx context.Context, _ *asynq.Task) error {
	type row struct {
		OrganizationID uuid.UUID
		BatchID        uuid.UUID
		LotNumber      string
		ExpiryDate     time.Time
		ProductName    string
		OnHand         money.Decimal
	}

	var rows []row
	err := w.db.WithContext(ctx).
		Table("batches b").
		Select(`b.organization_id, b.id AS batch_id, b.lot_number, b.expiry_date,
		        p.name AS product_name, COALESCE(SUM(si.quantity), 0) AS on_hand`).
		Joins("JOIN products p ON p.id = b.product_id").
		Joins("LEFT JOIN stock_items si ON si.batch_id = b.id").
		Where(`b.deleted_at IS NULL AND b.expiry_date IS NOT NULL
		       AND b.expiry_date <= current_date + interval '30 days'`).
		Group("b.organization_id, b.id, b.lot_number, b.expiry_date, p.name").
		Having("COALESCE(SUM(si.quantity), 0) > 0").
		Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("expiry scan: %w", err)
	}

	for _, r := range rows {
		batchID := r.BatchID
		days := int(time.Until(r.ExpiryDate).Hours() / 24)
		w.svc.Raise(ctx, Event{
			OrgID: r.OrganizationID,
			Type:  TypeExpiryWarning,
			Title: fmt.Sprintf("Batch expiring: %s", r.ProductName),
			Body: fmt.Sprintf("Lot %s of %s (%s on hand) expires in %d day(s), on %s.",
				r.LotNumber, r.ProductName, r.OnHand.String(), days, r.ExpiryDate.Format("2006-01-02")),
			Data: map[string]any{
				"batch_id": batchID, "lot_number": r.LotNumber,
				"expiry_date": r.ExpiryDate, "on_hand": r.OnHand,
			},
			EntityType: "batch",
			EntityID:   &batchID,
			Roles:      []auth.Role{auth.RoleAdmin, auth.RoleManager, auth.RoleWarehouseStaff},
			Email:      true,
			Push:       true,
		})
	}
	w.lg.Info().Int("alerts", len(rows)).Msg("expiry scan complete")
	return nil
}
