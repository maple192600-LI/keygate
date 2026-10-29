ALTER TABLE license_issuances
    ADD CONSTRAINT license_issuances_id_license UNIQUE (id, license_id);

ALTER TABLE license_deliveries
    ADD COLUMN license_id TEXT REFERENCES licenses(id);

ALTER TABLE license_deliveries DISABLE TRIGGER license_deliveries_immutable;
UPDATE license_deliveries delivery
SET license_id = issuance.license_id
FROM license_issuances issuance
WHERE issuance.id = delivery.issuance_id;
ALTER TABLE license_deliveries ENABLE TRIGGER license_deliveries_immutable;

ALTER TABLE license_deliveries
    ALTER COLUMN license_id SET NOT NULL,
    ALTER COLUMN issuance_id DROP NOT NULL,
    ADD CONSTRAINT license_deliveries_issuance_license
        FOREIGN KEY (issuance_id, license_id) REFERENCES license_issuances(id, license_id);

CREATE INDEX license_deliveries_license ON license_deliveries(license_id);
