-- Sales: customers, sales orders with pick-pack-ship progression, invoices,
-- and RMA returns.

CREATE TABLE customers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text        NOT NULL,
    name            text        NOT NULL,
    email           citext,
    phone           text,
    billing_address text,
    shipping_address text,
    tax_number      text,
    credit_limit    numeric(18, 4) NOT NULL DEFAULT 0 CHECK (credit_limit >= 0),
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX customers_org_code_key ON customers (organization_id, lower(code))
    WHERE deleted_at IS NULL;
CREATE INDEX customers_name_trgm_idx ON customers USING gin (name gin_trgm_ops);

CREATE TYPE sales_order_status AS ENUM (
    'draft',
    'confirmed',   -- stock reserved
    'picking',
    'packed',
    'shipped',     -- stock has left the building
    'delivered',
    'cancelled'
);

CREATE TABLE sales_orders (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid               NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text               NOT NULL,
    customer_id     uuid REFERENCES customers (id) ON DELETE RESTRICT,
    warehouse_id    uuid               NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    status          sales_order_status NOT NULL DEFAULT 'draft',
    currency        char(3)            NOT NULL DEFAULT 'USD',
    order_date      date               NOT NULL DEFAULT current_date,
    required_date   date,
    subtotal        numeric(18, 4)     NOT NULL DEFAULT 0 CHECK (subtotal >= 0),
    tax_total       numeric(18, 4)     NOT NULL DEFAULT 0 CHECK (tax_total >= 0),
    discount_total  numeric(18, 4)     NOT NULL DEFAULT 0 CHECK (discount_total >= 0),
    shipping_total  numeric(18, 4)     NOT NULL DEFAULT 0 CHECK (shipping_total >= 0),
    grand_total     numeric(18, 4)     NOT NULL DEFAULT 0 CHECK (grand_total >= 0),
    shipping_address text,
    tracking_number text,
    note            text,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    confirmed_at    timestamptz,
    picked_at       timestamptz,
    packed_at       timestamptz,
    shipped_at      timestamptz,
    delivered_at    timestamptz,
    cancelled_at    timestamptz,
    created_at      timestamptz        NOT NULL DEFAULT now(),
    updated_at      timestamptz        NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX sales_orders_org_code_key ON sales_orders (organization_id, code)
    WHERE deleted_at IS NULL;
CREATE INDEX sales_orders_org_status_idx ON sales_orders (organization_id, status, created_at DESC);
CREATE INDEX sales_orders_customer_idx ON sales_orders (customer_id);

CREATE TABLE sales_order_lines (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sales_order_id   uuid           NOT NULL REFERENCES sales_orders (id) ON DELETE CASCADE,
    product_id       uuid           NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    variant_id       uuid REFERENCES product_variants (id) ON DELETE RESTRICT,
    description      text,
    quantity         numeric(18, 4) NOT NULL CHECK (quantity > 0),
    quantity_shipped numeric(18, 4) NOT NULL DEFAULT 0 CHECK (quantity_shipped >= 0),
    unit_price       numeric(18, 4) NOT NULL CHECK (unit_price >= 0),
    tax_rate         numeric(9, 4)  NOT NULL DEFAULT 0 CHECK (tax_rate >= 0),
    discount_rate    numeric(9, 4)  NOT NULL DEFAULT 0 CHECK (discount_rate >= 0 AND discount_rate <= 100),
    line_total       numeric(18, 4) NOT NULL DEFAULT 0 CHECK (line_total >= 0),
    -- Cost of goods sold, resolved from FIFO layers at ship time. Kept on the
    -- line so historical margin does not move when costs change.
    cogs_total       numeric(18, 4),
    created_at       timestamptz    NOT NULL DEFAULT now(),
    updated_at       timestamptz    NOT NULL DEFAULT now(),
    CONSTRAINT so_lines_no_over_ship CHECK (quantity_shipped <= quantity)
);
CREATE INDEX sales_order_lines_so_idx ON sales_order_lines (sales_order_id);

CREATE TYPE invoice_status AS ENUM ('draft', 'issued', 'partially_paid', 'paid', 'void');

CREATE TABLE invoices (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid           NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text           NOT NULL,
    sales_order_id  uuid REFERENCES sales_orders (id) ON DELETE SET NULL,
    customer_id     uuid REFERENCES customers (id) ON DELETE RESTRICT,
    status          invoice_status NOT NULL DEFAULT 'draft',
    currency        char(3)        NOT NULL DEFAULT 'USD',
    issued_at       timestamptz,
    due_at          timestamptz,
    total           numeric(18, 4) NOT NULL DEFAULT 0 CHECK (total >= 0),
    amount_paid     numeric(18, 4) NOT NULL DEFAULT 0 CHECK (amount_paid >= 0),
    note            text,
    created_at      timestamptz    NOT NULL DEFAULT now(),
    updated_at      timestamptz    NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX invoices_org_code_key ON invoices (organization_id, code)
    WHERE deleted_at IS NULL;
CREATE INDEX invoices_org_status_idx ON invoices (organization_id, status);

CREATE TYPE return_status AS ENUM ('requested', 'approved', 'rejected', 'received', 'closed');

CREATE TABLE sales_returns (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid          NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text          NOT NULL,
    sales_order_id  uuid          NOT NULL REFERENCES sales_orders (id) ON DELETE RESTRICT,
    customer_id     uuid REFERENCES customers (id) ON DELETE RESTRICT,
    warehouse_id    uuid          NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    status          return_status NOT NULL DEFAULT 'requested',
    reason          text          NOT NULL,
    -- Damaged goods come back but must not re-enter sellable stock; that
    -- decision is recorded per return.
    restock         boolean       NOT NULL DEFAULT true,
    requested_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    approved_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    approved_at     timestamptz,
    received_at     timestamptz,
    refund_total    numeric(18, 4) NOT NULL DEFAULT 0 CHECK (refund_total >= 0),
    created_at      timestamptz   NOT NULL DEFAULT now(),
    updated_at      timestamptz   NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX sales_returns_org_code_key ON sales_returns (organization_id, code)
    WHERE deleted_at IS NULL;
CREATE INDEX sales_returns_order_idx ON sales_returns (sales_order_id);

CREATE TABLE sales_return_lines (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sales_return_id     uuid           NOT NULL REFERENCES sales_returns (id) ON DELETE CASCADE,
    sales_order_line_id uuid           NOT NULL REFERENCES sales_order_lines (id) ON DELETE RESTRICT,
    product_id          uuid           NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    variant_id          uuid REFERENCES product_variants (id) ON DELETE RESTRICT,
    batch_id            uuid REFERENCES batches (id) ON DELETE SET NULL,
    quantity            numeric(18, 4) NOT NULL CHECK (quantity > 0),
    unit_price          numeric(18, 4) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
    condition           text,
    created_at          timestamptz    NOT NULL DEFAULT now(),
    updated_at          timestamptz    NOT NULL DEFAULT now()
);
CREATE INDEX sales_return_lines_return_idx ON sales_return_lines (sales_return_id);
