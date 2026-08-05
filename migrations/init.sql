-- OML AI Quotation Generator — schema
-- Run with: psql "$DATABASE_URL" -f migrations/0001_init.sql

CREATE TABLE IF NOT EXISTS rfq_intakes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel         TEXT NOT NULL CHECK (channel IN ('whatsapp', 'email', 'tender')),
    raw_text        TEXT NOT NULL,
    raw_meta        JSONB NOT NULL DEFAULT '{}',   -- sender, subject, source URL, attachment refs, etc.
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rfq_extractions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    intake_id           UUID NOT NULL REFERENCES rfq_intakes(id),
    customer_name       TEXT,
    pickup_location     TEXT,
    destination         TEXT,
    mobilization_point  TEXT,
    cargo_type          TEXT,
    weight_or_volume    TEXT,
    delivery_urgency    TEXT,
    special_handling    TEXT,
    distance_km         NUMERIC,
    field_confidence    JSONB NOT NULL DEFAULT '{}', -- {"customer_name": 0.93, ...}
    missing_fields      TEXT[] NOT NULL DEFAULT '{}',
    raw_di_response     JSONB,                        -- full Azure DI response, kept for audit/debugging
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS routes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    origin              TEXT NOT NULL,
    destination         TEXT NOT NULL,
    mobilization_point  TEXT,
    distance_km         NUMERIC NOT NULL,        -- round trip
    avg_km_per_day      NUMERIC NOT NULL DEFAULT 300,
    loading_days        NUMERIC NOT NULL DEFAULT 2,
    UNIQUE (origin, destination)
);

CREATE TABLE IF NOT EXISTS cost_rates (
    id                      INT PRIMARY KEY DEFAULT 1,
    diesel_price_per_l      NUMERIC NOT NULL,
    fuel_consumption_per_km NUMERIC NOT NULL,   -- litres/km
    depreciation_per_day    NUMERIC NOT NULL,
    maintenance_per_km      NUMERIC NOT NULL,
    insurance_per_day       NUMERIC NOT NULL,
    driver_salary_per_day   NUMERIC NOT NULL,
    other_flat_cost         NUMERIC NOT NULL,   -- county cess / security, per trip
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (id = 1) -- singleton row, edited live from the admin panel (rubric C2)
);

CREATE TABLE IF NOT EXISTS customer_agreements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_name   TEXT NOT NULL UNIQUE,
    discount_pct    NUMERIC NOT NULL DEFAULT 0,   -- applied off the standard quoted price
    notes           TEXT
);

CREATE TABLE IF NOT EXISTS margin_thresholds (
    id                  INT PRIMARY KEY DEFAULT 1,
    target_margin_pct   NUMERIC NOT NULL,   -- what pricing aims for
    auto_approve_above  NUMERIC NOT NULL,   -- margin % >= this: auto-approve
    manager_review_above NUMERIC NOT NULL,  -- margin % >= this (and below auto-approve): manager
    -- below manager_review_above: escalate to director
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (id = 1)
);

CREATE TABLE IF NOT EXISTS quotes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    extraction_id       UUID NOT NULL REFERENCES rfq_extractions(id),
    quote_number        TEXT NOT NULL UNIQUE,
    cost_breakdown      JSONB NOT NULL,   -- {fuel, depreciation, maintenance, insurance, driver_salary, other, total_cost}
    total_cost          NUMERIC NOT NULL,
    selling_price        NUMERIC NOT NULL,
    achieved_margin_pct  NUMERIC NOT NULL,
    status              TEXT NOT NULL DEFAULT 'draft'
                        CHECK (status IN ('draft', 'pending_manager', 'pending_director', 'approved', 'rejected')),
    decision_notes      TEXT,
    decided_by          TEXT,
    pdf_url             TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at          TIMESTAMPTZ
);

-- Seed: the exact route/parameters from OML's provided sample dataset
-- (Nairobi -> Kisumu, mobilization from Mai Mahiu, 740km round trip, 300km/day, 4-day TAT)
INSERT INTO routes (origin, destination, mobilization_point, distance_km, avg_km_per_day, loading_days)
VALUES ('Nairobi', 'Kisumu', 'Mai Mahiu', 740, 300, 2)
ON CONFLICT (origin, destination) DO NOTHING;

-- Seed: starting cost rates — realistic Kenya trucking placeholders.
-- SWAP THESE for OML's real figures before the live demo if they're provided.
INSERT INTO cost_rates (id, diesel_price_per_l, fuel_consumption_per_km, depreciation_per_day,
                         maintenance_per_km, insurance_per_day, driver_salary_per_day, other_flat_cost)
VALUES (1, 182, 0.38, 3000, 18, 1200, 2500, 4000)
ON CONFLICT (id) DO NOTHING;

INSERT INTO margin_thresholds (id, target_margin_pct, auto_approve_above, manager_review_above)
VALUES (1, 18, 20, 12)
ON CONFLICT (id) DO NOTHING;
