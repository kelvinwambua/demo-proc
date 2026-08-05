-- OML Quote Project — Initial Schema
-- Run with: psql "$DATABASE_URL" -f migrations/0001_init.sql

CREATE TABLE IF NOT EXISTS rfqs (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_channel          TEXT NOT NULL,
    raw_text              TEXT NOT NULL,
    customer_name         TEXT,
    cargo_from            TEXT,
    destination           TEXT,
    mobilization_point     TEXT,
    cargo_type            TEXT,
    weight_volume         TEXT,
    delivery_urgency      TEXT,
    special_handling      TEXT,
    distance_km_roundtrip NUMERIC,
    avg_km_per_day        NUMERIC,
    loading_days          NUMERIC,
    days_taken            NUMERIC,
    extraction_confidence NUMERIC,
    unresolved_fields     TEXT[] NOT NULL DEFAULT '{}',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS quotes (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rfq_id                  UUID NOT NULL REFERENCES rfqs(id) ON DELETE CASCADE,
    fuel_cost               BYTEA NOT NULL,
    depreciation_cost       BYTEA NOT NULL,
    maintenance_cost        BYTEA NOT NULL,
    insurance_cost          BYTEA NOT NULL,
    driver_salary_cost      BYTEA NOT NULL,
    other_costs             BYTEA NOT NULL,
    total_cost              BYTEA NOT NULL,
    target_margin_pct       BYTEA NOT NULL,
    quoted_price            BYTEA NOT NULL,
    actual_margin_pct       BYTEA NOT NULL,
    approval_status         TEXT NOT NULL DEFAULT 'pending_manager'
                            CHECK (approval_status IN ('auto_approved','pending_manager','pending_director','rejected')),
    approved_by             TEXT,
    approved_at           TIMESTAMPTZ,
    pdf_url                 TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_quotes_rfq_id ON quotes(rfq_id);
CREATE INDEX IF NOT EXISTS idx_quotes_approval_status ON quotes(approval_status);

CREATE TABLE IF NOT EXISTS pricing_config (
    id                              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    route_key                       TEXT,
    customer_key                    TEXT,
    epra_diesel_rate                BYTEA NOT NULL,
    fuel_consumption_km_per_l       BYTEA NOT NULL,
    depreciation_rate_per_day           BYTEA NOT NULL,
    maintenance_rate_per_km          BYTEA NOT NULL,
    insurance_rate_per_day              BYTEA NOT NULL,
    driver_rate_per_day              BYTEA NOT NULL,
    other_costs_flat                 BYTEA NOT NULL,
    target_margin_pct                BYTEA,
    auto_approve_margin_floor         BYTEA NOT NULL,
    manager_review_margin_floor       BYTEA NOT NULL,
    director_escalation_margin_floor BYTEA NOT NULL,
    updated_by                      TEXT,
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (route_key, customer_key)
);

CREATE TABLE IF NOT EXISTS audit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type TEXT NOT NULL,
    entity_id   UUID NOT NULL,
    action      TEXT NOT NULL,
    actor       TEXT NOT NULL,
    detail      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_entity ON audit_log(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action);
CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log(created_at);

-- Seed: GLOBAL DEFAULT pricing_config (route_key=NULL, customer_key=NULL)
-- Numeric values stored as text-representation bytea; application layer properly
-- re-encrypts with ADMIN_SESSION_SECRET on first write via admin panel.
INSERT INTO pricing_config (
    route_key, customer_key,
    epra_diesel_rate, fuel_consumption_km_per_l, depreciation_rate_per_day,
    maintenance_rate_per_km, insurance_rate_per_day, driver_rate_per_day,
    other_costs_flat, target_margin_pct,
    auto_approve_margin_floor, manager_review_margin_floor, director_escalation_margin_floor
) VALUES (
    NULL, NULL,
    '182'::bytea,       '2.63'::bytea,  '3000'::bytea,
    '18'::bytea,        '1200'::bytea,  '2500'::bytea,
    '4000'::bytea,      '0.18'::bytea,
    '0.20'::bytea,      '0.12'::bytea,  '0.05'::bytea
) ON CONFLICT (route_key, customer_key) DO NOTHING;

-- Seed: Nairobi-Kisumu route override (route_key='Nairobi-Kisumu', customer_key=NULL)
-- Overrides: maintenance_rate_per_km=20; other fields inherit at read-time via
-- GetPricingConfig merge logic in application.
INSERT INTO pricing_config (
    route_key, customer_key,
    epra_diesel_rate, fuel_consumption_km_per_l, depreciation_rate_per_day,
    maintenance_rate_per_km, insurance_rate_per_day, driver_rate_per_day,
    other_costs_flat, target_margin_pct,
    auto_approve_margin_floor, manager_review_margin_floor, director_escalation_margin_floor
) VALUES (
    'Nairobi-Kisumu', NULL,
    '182'::bytea,       '2.63'::bytea,  '3000'::bytea,
    '20'::bytea,        '1200'::bytea,  '2500'::bytea,
    '4000'::bytea,      '0.18'::bytea,
    '0.20'::bytea,      '0.12'::bytea,  '0.05'::bytea
) ON CONFLICT (route_key, customer_key) DO NOTHING;

-- Seed: Sample customer override (customer_key='Acme Corp', route_key=NULL)
INSERT INTO pricing_config (
    route_key, customer_key,
    epra_diesel_rate, fuel_consumption_km_per_l, depreciation_rate_per_day,
    maintenance_rate_per_km, insurance_rate_per_day, driver_rate_per_day,
    other_costs_flat, target_margin_pct,
    auto_approve_margin_floor, manager_review_margin_floor, director_escalation_margin_floor
) VALUES (
    NULL, 'Acme Corp',
    '182'::bytea,       '2.63'::bytea,  '3000'::bytea,
    '18'::bytea,        '1200'::bytea,  '2500'::bytea,
    '4000'::bytea,      '0.18'::bytea,
    '0.20'::bytea,      '0.12'::bytea,  '0.05'::bytea
) ON CONFLICT (route_key, customer_key) DO NOTHING;
