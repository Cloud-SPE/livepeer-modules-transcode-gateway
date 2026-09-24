ALTER TABLE capabilities
    ADD COLUMN protocol text NOT NULL DEFAULT '',
    ADD COLUMN work_unit text NOT NULL DEFAULT '',
    ADD COLUMN units_per_price numeric(20,0),
    ADD COLUMN work_unit_estimator_json jsonb,
    ADD COLUMN job_json jsonb,
    ADD COLUMN session_json jsonb;

ALTER TABLE capabilities ADD CONSTRAINT capabilities_units_per_price_positive
    CHECK (units_per_price IS NULL OR units_per_price > 0);

-- Old snapshots guessed an interaction mode and omitted the price denominator.
-- Keep them unavailable until a successful authoritative LOC refresh.
UPDATE capabilities SET active = false, interaction_mode = NULL;
