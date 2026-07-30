-- Product catalog: categories, units of measure, suppliers, products, variants.

CREATE TABLE categories (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    parent_id       uuid REFERENCES categories (id) ON DELETE SET NULL,
    name            text        NOT NULL,
    code            text,
    description     text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE INDEX categories_org_idx ON categories (organization_id);
CREATE UNIQUE INDEX categories_org_name_key ON categories (organization_id, lower(name))
    WHERE deleted_at IS NULL;

CREATE TABLE units (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text        NOT NULL,
    name            text        NOT NULL,
    -- Decimal places allowed for quantities in this unit; 0 forces whole units
    -- (you cannot ship half a laptop).
    precision       smallint    NOT NULL DEFAULT 0 CHECK (precision BETWEEN 0 AND 4),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX units_org_code_key ON units (organization_id, lower(code))
    WHERE deleted_at IS NULL;

CREATE TABLE suppliers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    code            text        NOT NULL,
    name            text        NOT NULL,
    contact_name    text,
    email           citext,
    phone           text,
    address         text,
    tax_number      text,
    payment_terms   text,
    lead_time_days  integer     NOT NULL DEFAULT 0 CHECK (lead_time_days >= 0),
    is_active       boolean     NOT NULL DEFAULT true,
    notes           text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE INDEX suppliers_org_idx ON suppliers (organization_id);
CREATE UNIQUE INDEX suppliers_org_code_key ON suppliers (organization_id, lower(code))
    WHERE deleted_at IS NULL;
CREATE INDEX suppliers_name_trgm_idx ON suppliers USING gin (name gin_trgm_ops);

CREATE TABLE products (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid           NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    sku               text           NOT NULL,
    name              text           NOT NULL,
    description       text,
    barcode           text,
    category_id       uuid REFERENCES categories (id) ON DELETE SET NULL,
    unit_id           uuid REFERENCES units (id) ON DELETE SET NULL,
    supplier_id       uuid REFERENCES suppliers (id) ON DELETE SET NULL,
    cost_price        numeric(18, 4) NOT NULL DEFAULT 0 CHECK (cost_price >= 0),
    sell_price        numeric(18, 4) NOT NULL DEFAULT 0 CHECK (sell_price >= 0),
    min_stock         numeric(18, 4) NOT NULL DEFAULT 0 CHECK (min_stock >= 0),
    max_stock         numeric(18, 4) CHECK (max_stock IS NULL OR max_stock >= min_stock),
    reorder_quantity  numeric(18, 4) NOT NULL DEFAULT 0 CHECK (reorder_quantity >= 0),
    -- When true, stock for this product must always be attributed to a batch,
    -- which is what makes expiry tracking enforceable rather than advisory.
    track_batches     boolean        NOT NULL DEFAULT false,
    shelf_life_days   integer CHECK (shelf_life_days IS NULL OR shelf_life_days > 0),
    is_active         boolean        NOT NULL DEFAULT true,
    created_at        timestamptz    NOT NULL DEFAULT now(),
    updated_at        timestamptz    NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE INDEX products_org_idx ON products (organization_id);
CREATE UNIQUE INDEX products_org_sku_key ON products (organization_id, lower(sku))
    WHERE deleted_at IS NULL;
-- Barcode lookup is the hot path for the mobile scanner.
CREATE UNIQUE INDEX products_org_barcode_key ON products (organization_id, barcode)
    WHERE deleted_at IS NULL AND barcode IS NOT NULL;
CREATE INDEX products_name_trgm_idx ON products USING gin (name gin_trgm_ops);
CREATE INDEX products_category_idx ON products (category_id) WHERE deleted_at IS NULL;

CREATE TABLE product_variants (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid           NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    product_id      uuid           NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    sku             text           NOT NULL,
    name            text           NOT NULL,
    barcode         text,
    -- {"size": "L", "color": "red"} — kept as jsonb because the attribute set
    -- differs per product family.
    attributes      jsonb          NOT NULL DEFAULT '{}'::jsonb,
    cost_price      numeric(18, 4),
    sell_price      numeric(18, 4),
    min_stock       numeric(18, 4) NOT NULL DEFAULT 0 CHECK (min_stock >= 0),
    max_stock       numeric(18, 4),
    is_active       boolean        NOT NULL DEFAULT true,
    created_at      timestamptz    NOT NULL DEFAULT now(),
    updated_at      timestamptz    NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE INDEX product_variants_product_idx ON product_variants (product_id);
CREATE UNIQUE INDEX product_variants_org_sku_key ON product_variants (organization_id, lower(sku))
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX product_variants_org_barcode_key ON product_variants (organization_id, barcode)
    WHERE deleted_at IS NULL AND barcode IS NOT NULL;

CREATE TABLE product_images (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    product_id      uuid        NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    url             text        NOT NULL,
    alt_text        text,
    is_primary      boolean     NOT NULL DEFAULT false,
    sort_order      integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE INDEX product_images_product_idx ON product_images (product_id);
CREATE UNIQUE INDEX product_images_one_primary_key ON product_images (product_id)
    WHERE is_primary AND deleted_at IS NULL;
