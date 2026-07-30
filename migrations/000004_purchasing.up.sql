-- Purchasing: purchase orders with an approval workflow, and goods-received
-- notes supporting partial receipt.

CREATE TYPE purchase_order_status AS ENUM (
    'draft',
    'pending_approval',
    'approved',
    'rejected',
    'partially_received',
    'received',
    'cancelled'
);

CREATE TABLE purchase_orders (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid                  NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code              text                  NOT NULL,
    supplier_id       uuid                  NOT NULL REFERENCES suppliers (id) ON DELETE RESTRICT,
    warehouse_id      uuid                  NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    status            purchase_order_status NOT NULL DEFAULT 'draft',
    currency          char(3)               NOT NULL DEFAULT 'USD',
    expected_date     date,
    -- Totals are stored rather than always recomputed: an approved PO is a
    -- financial record and must not change if a product's price later does.
    subtotal          numeric(18, 4)        NOT NULL DEFAULT 0 CHECK (subtotal >= 0),
    tax_total         numeric(18, 4)        NOT NULL DEFAULT 0 CHECK (tax_total >= 0),
    discount_total    numeric(18, 4)        NOT NULL DEFAULT 0 CHECK (discount_total >= 0),
    shipping_total    numeric(18, 4)        NOT NULL DEFAULT 0 CHECK (shipping_total >= 0),
    grand_total       numeric(18, 4)        NOT NULL DEFAULT 0 CHECK (grand_total >= 0),
    note              text,
    created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    submitted_at      timestamptz,
    approved_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    approved_at       timestamptz,
    rejected_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    rejected_at       timestamptz,
    rejection_reason  text,
    created_at        timestamptz           NOT NULL DEFAULT now(),
    updated_at        timestamptz           NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE UNIQUE INDEX purchase_orders_org_code_key ON purchase_orders (organization_id, code)
    WHERE deleted_at IS NULL;
CREATE INDEX purchase_orders_org_status_idx ON purchase_orders (organization_id, status, created_at DESC);
CREATE INDEX purchase_orders_supplier_idx ON purchase_orders (supplier_id);

CREATE TABLE purchase_order_lines (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_order_id uuid           NOT NULL REFERENCES purchase_orders (id) ON DELETE CASCADE,
    product_id        uuid           NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    variant_id        uuid REFERENCES product_variants (id) ON DELETE RESTRICT,
    description       text,
    quantity_ordered  numeric(18, 4) NOT NULL CHECK (quantity_ordered > 0),
    quantity_received numeric(18, 4) NOT NULL DEFAULT 0 CHECK (quantity_received >= 0),
    unit_price        numeric(18, 4) NOT NULL CHECK (unit_price >= 0),
    tax_rate          numeric(9, 4)  NOT NULL DEFAULT 0 CHECK (tax_rate >= 0),
    discount_rate     numeric(9, 4)  NOT NULL DEFAULT 0 CHECK (discount_rate >= 0 AND discount_rate <= 100),
    line_total        numeric(18, 4) NOT NULL DEFAULT 0 CHECK (line_total >= 0),
    created_at        timestamptz    NOT NULL DEFAULT now(),
    updated_at        timestamptz    NOT NULL DEFAULT now(),
    -- Over-receipt is rejected at the application layer with a clear error;
    -- this constraint is the backstop.
    CONSTRAINT po_lines_no_over_receipt CHECK (quantity_received <= quantity_ordered)
);
CREATE INDEX purchase_order_lines_po_idx ON purchase_order_lines (purchase_order_id);

CREATE TABLE goods_receipts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code              text        NOT NULL,
    purchase_order_id uuid        NOT NULL REFERENCES purchase_orders (id) ON DELETE RESTRICT,
    warehouse_id      uuid        NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    supplier_ref      text,
    note              text,
    received_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    received_at       timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE UNIQUE INDEX goods_receipts_org_code_key ON goods_receipts (organization_id, code)
    WHERE deleted_at IS NULL;
CREATE INDEX goods_receipts_po_idx ON goods_receipts (purchase_order_id);

CREATE TABLE goods_receipt_lines (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goods_receipt_id       uuid           NOT NULL REFERENCES goods_receipts (id) ON DELETE CASCADE,
    purchase_order_line_id uuid           NOT NULL REFERENCES purchase_order_lines (id) ON DELETE RESTRICT,
    product_id             uuid           NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    variant_id             uuid REFERENCES product_variants (id) ON DELETE RESTRICT,
    batch_id               uuid REFERENCES batches (id) ON DELETE SET NULL,
    location_id            uuid REFERENCES locations (id) ON DELETE SET NULL,
    quantity               numeric(18, 4) NOT NULL CHECK (quantity > 0),
    -- Captured per receipt because the same product can arrive at different
    -- costs; this is the unit cost that seeds the FIFO layer.
    unit_cost              numeric(18, 4) NOT NULL CHECK (unit_cost >= 0),
    created_at             timestamptz    NOT NULL DEFAULT now(),
    updated_at             timestamptz    NOT NULL DEFAULT now()
);
CREATE INDEX goods_receipt_lines_grn_idx ON goods_receipt_lines (goods_receipt_id);
