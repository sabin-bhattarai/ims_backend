-- Warehouses, bin locations, batches, the stock ledger and FIFO cost layers.

CREATE TABLE warehouses (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text        NOT NULL,
    name            text        NOT NULL,
    address         text,
    city            text,
    country         text,
    manager_id      uuid REFERENCES users (id) ON DELETE SET NULL,
    is_default      boolean     NOT NULL DEFAULT false,
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE INDEX warehouses_org_idx ON warehouses (organization_id);
CREATE UNIQUE INDEX warehouses_org_code_key ON warehouses (organization_id, lower(code))
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX warehouses_one_default_key ON warehouses (organization_id)
    WHERE is_default AND deleted_at IS NULL;

CREATE TYPE location_kind AS ENUM ('zone', 'aisle', 'rack', 'shelf', 'bin');

CREATE TABLE locations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid          NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    warehouse_id    uuid          NOT NULL REFERENCES warehouses (id) ON DELETE CASCADE,
    parent_id       uuid REFERENCES locations (id) ON DELETE SET NULL,
    code            text          NOT NULL,
    name            text,
    kind            location_kind NOT NULL DEFAULT 'bin',
    -- Denormalised "A/A1/R3/S2/B14" path, so the mobile app can render a
    -- breadcrumb without walking the tree.
    path            text,
    is_active       boolean       NOT NULL DEFAULT true,
    created_at      timestamptz   NOT NULL DEFAULT now(),
    updated_at      timestamptz   NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE INDEX locations_warehouse_idx ON locations (warehouse_id);
CREATE UNIQUE INDEX locations_wh_code_key ON locations (warehouse_id, lower(code))
    WHERE deleted_at IS NULL;

CREATE TABLE batches (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    product_id      uuid        NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    lot_number      text        NOT NULL,
    expiry_date     date,
    manufactured_at date,
    supplier_id     uuid REFERENCES suppliers (id) ON DELETE SET NULL,
    received_at     timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX batches_product_lot_key ON batches (product_id, lower(lot_number))
    WHERE deleted_at IS NULL;
-- Drives the expiry alert scan.
CREATE INDEX batches_expiry_idx ON batches (organization_id, expiry_date)
    WHERE deleted_at IS NULL AND expiry_date IS NOT NULL;

-- Current on-hand quantity per (product, variant, warehouse, location, batch).
-- NULLS NOT DISTINCT (PG15+) is what makes the natural key work: without it,
-- rows with a NULL location or batch would duplicate freely.
CREATE TABLE stock_items (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid           NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    product_id        uuid           NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    variant_id        uuid REFERENCES product_variants (id) ON DELETE CASCADE,
    warehouse_id      uuid           NOT NULL REFERENCES warehouses (id) ON DELETE CASCADE,
    location_id       uuid REFERENCES locations (id) ON DELETE SET NULL,
    batch_id          uuid REFERENCES batches (id) ON DELETE SET NULL,
    quantity          numeric(18, 4) NOT NULL DEFAULT 0,
    -- Allocated to confirmed sales orders but not yet picked. Available =
    -- quantity - reserved_quantity.
    reserved_quantity numeric(18, 4) NOT NULL DEFAULT 0 CHECK (reserved_quantity >= 0),
    created_at        timestamptz    NOT NULL DEFAULT now(),
    updated_at        timestamptz    NOT NULL DEFAULT now(),
    CONSTRAINT stock_items_non_negative CHECK (quantity >= 0),
    CONSTRAINT stock_items_natural_key
        UNIQUE NULLS NOT DISTINCT (product_id, variant_id, warehouse_id, location_id, batch_id)
);
CREATE INDEX stock_items_org_product_idx ON stock_items (organization_id, product_id);
CREATE INDEX stock_items_warehouse_idx ON stock_items (warehouse_id);

CREATE TYPE movement_type AS ENUM (
    'stock_in',      -- purchase receipt / goods received
    'stock_out',     -- sale, issue, dispatch
    'adjustment',    -- manual correction (damage, shrinkage, found stock)
    'transfer_out',  -- leaving a warehouse
    'transfer_in',   -- arriving at a warehouse
    'count',         -- cycle-count reconciliation
    'return_in',     -- customer return restocked
    'return_out'     -- return to supplier
);

-- Append-only ledger. Every stock-affecting action writes exactly one row per
-- affected stock item, recording who did it and the before/after quantity.
-- Nothing in the application updates or deletes these rows.
CREATE TABLE stock_movements (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid           NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    type              movement_type  NOT NULL,
    product_id        uuid           NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    variant_id        uuid REFERENCES product_variants (id) ON DELETE RESTRICT,
    warehouse_id      uuid           NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    location_id       uuid REFERENCES locations (id) ON DELETE SET NULL,
    batch_id          uuid REFERENCES batches (id) ON DELETE SET NULL,
    -- Signed: negative for outbound. quantity_after = quantity_before + delta.
    quantity_delta    numeric(18, 4) NOT NULL CHECK (quantity_delta <> 0),
    quantity_before   numeric(18, 4) NOT NULL,
    quantity_after    numeric(18, 4) NOT NULL,
    unit_cost         numeric(18, 4),
    reason            text,
    note              text,
    reference_type    text,
    reference_id      uuid,
    actor_id          uuid REFERENCES users (id) ON DELETE SET NULL,
    -- Idempotency key supplied by the mobile app. A queued offline scan may be
    -- retried after a network failure; the unique index makes the retry a
    -- no-op instead of double-counting stock.
    client_request_id text,
    -- When the action happened on the device, which can be well before it was
    -- synced.
    occurred_at       timestamptz    NOT NULL DEFAULT now(),
    created_at        timestamptz    NOT NULL DEFAULT now()
);
CREATE INDEX stock_movements_org_created_idx ON stock_movements (organization_id, created_at DESC);
CREATE INDEX stock_movements_product_idx ON stock_movements (product_id, created_at DESC);
CREATE INDEX stock_movements_warehouse_idx ON stock_movements (warehouse_id, created_at DESC);
CREATE INDEX stock_movements_reference_idx ON stock_movements (reference_type, reference_id);
CREATE UNIQUE INDEX stock_movements_client_request_key
    ON stock_movements (organization_id, client_request_id)
    WHERE client_request_id IS NOT NULL;

-- FIFO cost layers. Each receipt creates a layer; each issue consumes the
-- oldest remaining layers, which is what makes FIFO valuation and COGS exact
-- rather than an estimate derived from an average.
CREATE TABLE cost_layers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid           NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    product_id      uuid           NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    variant_id      uuid REFERENCES product_variants (id) ON DELETE CASCADE,
    warehouse_id    uuid           NOT NULL REFERENCES warehouses (id) ON DELETE CASCADE,
    batch_id        uuid REFERENCES batches (id) ON DELETE SET NULL,
    quantity        numeric(18, 4) NOT NULL CHECK (quantity > 0),
    remaining       numeric(18, 4) NOT NULL CHECK (remaining >= 0),
    unit_cost       numeric(18, 4) NOT NULL CHECK (unit_cost >= 0),
    received_at     timestamptz    NOT NULL DEFAULT now(),
    movement_id     uuid REFERENCES stock_movements (id) ON DELETE SET NULL,
    created_at      timestamptz    NOT NULL DEFAULT now(),
    CONSTRAINT cost_layers_remaining_lte_quantity CHECK (remaining <= quantity)
);
-- Consumption order: oldest layer with stock left, first.
CREATE INDEX cost_layers_fifo_idx
    ON cost_layers (organization_id, product_id, warehouse_id, received_at)
    WHERE remaining > 0;

CREATE TYPE transfer_status AS ENUM ('draft', 'in_transit', 'received', 'cancelled');

CREATE TABLE stock_transfers (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    uuid            NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code               text            NOT NULL,
    from_warehouse_id  uuid            NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    to_warehouse_id    uuid            NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    status             transfer_status NOT NULL DEFAULT 'draft',
    note               text,
    created_by         uuid REFERENCES users (id) ON DELETE SET NULL,
    dispatched_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    dispatched_at      timestamptz,
    received_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    received_at        timestamptz,
    created_at         timestamptz     NOT NULL DEFAULT now(),
    updated_at         timestamptz     NOT NULL DEFAULT now(),
    deleted_at         timestamptz,
    CONSTRAINT stock_transfers_distinct_warehouses CHECK (from_warehouse_id <> to_warehouse_id)
);
CREATE UNIQUE INDEX stock_transfers_org_code_key ON stock_transfers (organization_id, code)
    WHERE deleted_at IS NULL;
CREATE INDEX stock_transfers_org_status_idx ON stock_transfers (organization_id, status);

CREATE TABLE stock_transfer_lines (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transfer_id   uuid           NOT NULL REFERENCES stock_transfers (id) ON DELETE CASCADE,
    product_id    uuid           NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    variant_id    uuid REFERENCES product_variants (id) ON DELETE RESTRICT,
    batch_id      uuid REFERENCES batches (id) ON DELETE SET NULL,
    quantity      numeric(18, 4) NOT NULL CHECK (quantity > 0),
    received_quantity numeric(18, 4) NOT NULL DEFAULT 0 CHECK (received_quantity >= 0),
    created_at    timestamptz    NOT NULL DEFAULT now(),
    updated_at    timestamptz    NOT NULL DEFAULT now()
);
CREATE INDEX stock_transfer_lines_transfer_idx ON stock_transfer_lines (transfer_id);

CREATE TYPE cycle_count_status AS ENUM ('draft', 'counting', 'review', 'completed', 'cancelled');

CREATE TABLE cycle_counts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid               NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text               NOT NULL,
    warehouse_id    uuid               NOT NULL REFERENCES warehouses (id) ON DELETE RESTRICT,
    location_id     uuid REFERENCES locations (id) ON DELETE SET NULL,
    status          cycle_count_status NOT NULL DEFAULT 'draft',
    note            text,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    completed_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    completed_at    timestamptz,
    created_at      timestamptz        NOT NULL DEFAULT now(),
    updated_at      timestamptz        NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX cycle_counts_org_code_key ON cycle_counts (organization_id, code)
    WHERE deleted_at IS NULL;

CREATE TABLE cycle_count_lines (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cycle_count_id    uuid           NOT NULL REFERENCES cycle_counts (id) ON DELETE CASCADE,
    product_id        uuid           NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    variant_id        uuid REFERENCES product_variants (id) ON DELETE RESTRICT,
    batch_id          uuid REFERENCES batches (id) ON DELETE SET NULL,
    location_id       uuid REFERENCES locations (id) ON DELETE SET NULL,
    -- Snapshotted when the count sheet is generated, so variance is measured
    -- against what the system believed at that moment.
    expected_quantity numeric(18, 4) NOT NULL DEFAULT 0,
    counted_quantity  numeric(18, 4),
    counted_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    counted_at        timestamptz,
    note              text,
    created_at        timestamptz    NOT NULL DEFAULT now(),
    updated_at        timestamptz    NOT NULL DEFAULT now()
);
CREATE INDEX cycle_count_lines_count_idx ON cycle_count_lines (cycle_count_id);
