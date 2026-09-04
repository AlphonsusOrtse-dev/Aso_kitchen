-- ============================================================
-- Aso Kitchen — PostgreSQL Schema
-- ============================================================

CREATE EXTENSION IF NOT EXISTS "pgcrypto"; -- for gen_random_uuid()

-- ---------- ENUM TYPES ----------
CREATE TYPE user_role        AS ENUM ('customer', 'staff', 'admin');
CREATE TYPE order_type       AS ENUM ('pickup', 'delivery');
CREATE TYPE order_status     AS ENUM (
    'pending',
    'confirmed',
    'preparing',
    'cooking',
    'ready',
    'fulfilled',
    'cancelled'
);
CREATE TYPE payment_status   AS ENUM ('unpaid', 'paid', 'refunded', 'failed');

-- ---------- USERS ----------
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    full_name       VARCHAR(150) NOT NULL,
    email           VARCHAR(150) UNIQUE NOT NULL,
    phone           VARCHAR(30)  UNIQUE,
    password_hash   TEXT NOT NULL,
    role            user_role NOT NULL DEFAULT 'customer',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- MENU CATEGORIES ----------
CREATE TABLE menu_categories (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    sort_order  INT NOT NULL DEFAULT 0
);

-- ---------- MENU ITEMS (with live stock status) ----------
CREATE TABLE menu_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    category_id     INT REFERENCES menu_categories(id) ON DELETE SET NULL,
    name            VARCHAR(150) NOT NULL,
    description     TEXT,
    price           NUMERIC(10,2) NOT NULL CHECK (price >= 0),
    image_url       TEXT,
    is_available    BOOLEAN NOT NULL DEFAULT TRUE,   -- real-time In Stock / Out of Stock toggle
    stock_quantity  INT NOT NULL DEFAULT 0,          -- optional finer-grained tracking
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_menu_items_category ON menu_items(category_id);
CREATE INDEX idx_menu_items_available ON menu_items(is_available);

-- ---------- ORDERS ----------
CREATE TABLE orders (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    order_type          order_type NOT NULL,
    status              order_status NOT NULL DEFAULT 'pending',
    scheduled_for       TIMESTAMPTZ,              -- pre-order pickup/delivery time (NULL = ASAP)
    delivery_address    TEXT,                     -- required if order_type = 'delivery'
    subtotal_amount     NUMERIC(10,2) NOT NULL DEFAULT 0,
    total_amount        NUMERIC(10,2) NOT NULL DEFAULT 0,
    payment_status      payment_status NOT NULL DEFAULT 'unpaid',
    notes               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_user ON orders(user_id);
CREATE INDEX idx_orders_status ON orders(status);
CREATE INDEX idx_orders_scheduled_for ON orders(scheduled_for);

-- ---------- ORDER ITEMS ----------
CREATE TABLE order_items (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id            UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    menu_item_id        UUID NOT NULL REFERENCES menu_items(id) ON DELETE RESTRICT,
    quantity            INT NOT NULL CHECK (quantity > 0),
    unit_price          NUMERIC(10,2) NOT NULL,   -- snapshot of price at order time
    subtotal            NUMERIC(10,2) NOT NULL,
    special_instructions TEXT
);

CREATE INDEX idx_order_items_order ON order_items(order_id);
CREATE INDEX idx_order_items_menu_item ON order_items(menu_item_id);

-- ---------- ORDER STATUS HISTORY (audit trail + drives the live progress bar) ----------
CREATE TABLE order_status_history (
    id          BIGSERIAL PRIMARY KEY,
    order_id    UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    status      order_status NOT NULL,
    changed_by  UUID REFERENCES users(id) ON DELETE SET NULL, -- staff member, NULL = system
    note        TEXT,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_status_history_order ON order_status_history(order_id, changed_at);

-- ---------- updated_at auto-touch trigger ----------
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_menu_items_updated_at BEFORE UPDATE ON menu_items
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_orders_updated_at BEFORE UPDATE ON orders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------- Auto-log status changes into history ----------
CREATE OR REPLACE FUNCTION log_order_status_change()
RETURNS TRIGGER AS $$
BEGIN
    IF (TG_OP = 'INSERT') OR (NEW.status IS DISTINCT FROM OLD.status) THEN
        INSERT INTO order_status_history (order_id, status, changed_at)
        VALUES (NEW.id, NEW.status, now());
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_orders_status_log
    AFTER INSERT OR UPDATE OF status ON orders
    FOR EACH ROW EXECUTE FUNCTION log_order_status_change();

-- ---------- Seed data (optional, for local dev) ----------
INSERT INTO menu_categories (name, description, sort_order) VALUES
  ('Soups & Swallow', 'Traditional soups served with a choice of swallow', 1),
  ('Grills', 'Grilled meats and fish', 2),
  ('Drinks', 'Beverages', 3);
