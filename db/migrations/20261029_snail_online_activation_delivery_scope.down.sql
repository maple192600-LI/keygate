DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM license_deliveries WHERE issuance_id IS NULL) THEN
        RAISE EXCEPTION 'cannot reverse license-code delivery history without losing records';
    END IF;
END $$;

DROP INDEX license_deliveries_license;
ALTER TABLE license_deliveries
    DROP CONSTRAINT license_deliveries_issuance_license,
    ALTER COLUMN issuance_id SET NOT NULL,
    DROP COLUMN license_id;
ALTER TABLE license_issuances DROP CONSTRAINT license_issuances_id_license;
