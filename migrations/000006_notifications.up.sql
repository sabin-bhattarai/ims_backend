-- In-app notifications, push device registry, and document number sequences.

CREATE TYPE notification_type AS ENUM (
    'low_stock',
    'expiry_warning',
    'po_pending_approval',
    'po_approved',
    'po_rejected',
    'transfer_received',
    'return_requested',
    'report_ready'
);

CREATE TYPE notification_channel AS ENUM ('in_app', 'email', 'push');

CREATE TABLE notifications (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid              NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    -- NULL means org-wide: every user with a qualifying role sees it.
    user_id         uuid REFERENCES users (id) ON DELETE CASCADE,
    type            notification_type NOT NULL,
    title           text              NOT NULL,
    body            text              NOT NULL,
    -- Deep-link payload, e.g. {"product_id": "...", "warehouse_id": "..."}.
    data            jsonb             NOT NULL DEFAULT '{}'::jsonb,
    entity_type     text,
    entity_id       uuid,
    read_at         timestamptz,
    created_at      timestamptz       NOT NULL DEFAULT now()
);
CREATE INDEX notifications_user_unread_idx ON notifications (user_id, created_at DESC)
    WHERE read_at IS NULL;
CREATE INDEX notifications_org_idx ON notifications (organization_id, created_at DESC);
-- Suppresses duplicate alerts: one open low-stock notification per entity, so
-- a product sitting below its threshold does not generate an alert per scan.
CREATE UNIQUE INDEX notifications_open_alert_key
    ON notifications (organization_id, type, entity_type, entity_id)
    WHERE read_at IS NULL AND entity_id IS NOT NULL;

CREATE TABLE device_tokens (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token           text        NOT NULL,
    platform        text        NOT NULL CHECK (platform IN ('android', 'ios', 'web')),
    device_name     text,
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX device_tokens_token_key ON device_tokens (token);
CREATE INDEX device_tokens_user_idx ON device_tokens (user_id);

-- Per-organization, per-document-type counters behind human-readable codes
-- (PO-2026-000123). Allocation uses SELECT ... FOR UPDATE so concurrent
-- requests cannot mint the same code.
CREATE TABLE document_sequences (
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    doc_type        text        NOT NULL,
    period          text        NOT NULL,
    last_value      bigint      NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, doc_type, period)
);
