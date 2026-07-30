-- Foundation: tenancy, identity, RBAC, audit.
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";
-- citext gives case-insensitive email uniqueness without lower() indexes.
CREATE EXTENSION IF NOT EXISTS "citext";

CREATE TABLE organizations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    slug         text        NOT NULL,
    currency     char(3)     NOT NULL DEFAULT 'USD',
    timezone     text        NOT NULL DEFAULT 'UTC',
    settings     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);
-- Partial unique indexes throughout: a soft-deleted row must not block reuse
-- of its natural key.
CREATE UNIQUE INDEX organizations_slug_key ON organizations (slug) WHERE deleted_at IS NULL;

-- Roles are a fixed enum rather than a table: RBAC is enforced identically in
-- the backend, web and mobile clients, so the set must not drift at runtime.
CREATE TYPE user_role AS ENUM ('admin', 'manager', 'warehouse_staff', 'viewer');
CREATE TYPE user_status AS ENUM ('active', 'invited', 'suspended');

CREATE TABLE users (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email             citext,
    full_name         text        NOT NULL,
    password_hash     text        NOT NULL,
    role              user_role   NOT NULL DEFAULT 'viewer',
    status            user_status NOT NULL DEFAULT 'active',
    phone             text,
    last_login_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE INDEX users_organization_id_idx ON users (organization_id);
CREATE UNIQUE INDEX users_email_key ON users (email) WHERE deleted_at IS NULL;

CREATE TABLE refresh_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Only a SHA-256 hash is stored: a database leak must not yield usable
    -- sessions.
    token_hash  text        NOT NULL,
    user_agent  text,
    ip_address  inet,
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    -- Set when this token is rotated, so replay of an old token is detectable.
    replaced_by uuid REFERENCES refresh_tokens (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX refresh_tokens_hash_key ON refresh_tokens (token_hash);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id) WHERE revoked_at IS NULL;

CREATE TABLE password_resets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash text        NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX password_resets_hash_key ON password_resets (token_hash);

-- Login attempts are recorded whether or not they succeed, including for
-- unknown emails, which is what makes brute-force patterns visible.
CREATE TABLE login_audits (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid REFERENCES users (id) ON DELETE SET NULL,
    email      citext      NOT NULL,
    success    boolean     NOT NULL,
    reason     text,
    ip_address inet,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_audits_email_idx ON login_audits (email, created_at DESC);
CREATE INDEX login_audits_user_idx ON login_audits (user_id, created_at DESC);

-- Generic audit trail. Stock-affecting actions additionally write an immutable
-- row to stock_movements; this table covers every other entity change.
CREATE TABLE audit_logs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    actor_id        uuid REFERENCES users (id) ON DELETE SET NULL,
    actor_email     citext,
    action          text        NOT NULL,
    entity_type     text        NOT NULL,
    entity_id       uuid,
    before          jsonb,
    after           jsonb,
    ip_address      inet,
    request_id      text,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_org_created_idx ON audit_logs (organization_id, created_at DESC);
CREATE INDEX audit_logs_entity_idx ON audit_logs (entity_type, entity_id, created_at DESC);
