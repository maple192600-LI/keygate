CREATE TABLE commercial_customers (
 id TEXT PRIMARY KEY,
 display_name TEXT NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 100),
 customer_type TEXT NOT NULL CHECK (customer_type IN ('company','person')),
 contact_name TEXT NOT NULL DEFAULT '', contact_method TEXT NOT NULL DEFAULT '',
 email TEXT, source_text TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
 issuance_enabled BOOLEAN NOT NULL DEFAULT true,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX commercial_customers_name ON commercial_customers(display_name);
CREATE TABLE offline_requests (
 request_id TEXT PRIMARY KEY, product_code TEXT NOT NULL,
 device_type TEXT NOT NULL CHECK (device_type='windows-machineguid-sha256/v1'),
 device_digest TEXT NOT NULL CHECK (device_digest ~ '^[0-9a-f]{64}$'), device_label TEXT NOT NULL DEFAULT '',
 raw_bytes BYTEA NOT NULL CHECK (octet_length(raw_bytes)<=65536), fingerprint TEXT NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
 created_by TEXT NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE licenses ALTER COLUMN email DROP NOT NULL;
ALTER TABLE licenses ADD COLUMN customer_id TEXT REFERENCES commercial_customers(id);
ALTER TABLE licenses ADD COLUMN fulfillment_mode TEXT NOT NULL DEFAULT 'online' CHECK (fulfillment_mode IN ('online','offline_file'));
ALTER TABLE licenses ADD COLUMN issuance_enabled BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE licenses ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 0 CHECK (current_revision BETWEEN 0 AND 9007199254740991);
ALTER TABLE licenses ADD COLUMN current_issuance_id TEXT;
ALTER TABLE licenses ADD COLUMN display_number TEXT UNIQUE;
CREATE SEQUENCE snail_license_display_seq;
ALTER TABLE licenses ADD CONSTRAINT licenses_fulfillment_contact CHECK (
 (fulfillment_mode='offline_file' AND customer_id IS NOT NULL) OR
 (fulfillment_mode='online' AND email IS NOT NULL AND btrim(email)<>'')
);
CREATE INDEX offline_licenses_customer ON licenses(customer_id) WHERE fulfillment_mode='offline_file';
CREATE INDEX offline_licenses_expiry ON licenses(valid_until) WHERE fulfillment_mode='offline_file';
CREATE TABLE offline_device_bindings (
 id TEXT PRIMARY KEY, license_id TEXT NOT NULL REFERENCES licenses(id),
 device_type TEXT NOT NULL CHECK (device_type='windows-machineguid-sha256/v1'),
 device_digest TEXT NOT NULL CHECK (device_digest ~ '^[0-9a-f]{64}$'), device_label TEXT NOT NULL DEFAULT '',
 registered_at TIMESTAMPTZ NOT NULL DEFAULT now(), superseded_at TIMESTAMPTZ,
 previous_binding_id TEXT REFERENCES offline_device_bindings(id)
);
CREATE UNIQUE INDEX offline_current_binding ON offline_device_bindings(license_id) WHERE superseded_at IS NULL;
CREATE TABLE license_issuances (
 id TEXT PRIMARY KEY, license_id TEXT NOT NULL REFERENCES licenses(id), revision BIGINT NOT NULL CHECK (revision BETWEEN 1 AND 9007199254740991),
 operation_id TEXT NOT NULL UNIQUE, operation_hash TEXT NOT NULL CHECK (operation_hash ~ '^[0-9a-f]{64}$'),
 kind TEXT NOT NULL CHECK (kind IN ('initial','renew','transfer')),
 request_id TEXT NOT NULL REFERENCES offline_requests(request_id), binding_id TEXT NOT NULL REFERENCES offline_device_bindings(id),
 key_id TEXT NOT NULL CHECK (key_id ~ '^lic-[0-9a-f]{64}$'),
 not_before TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ, issued_at TIMESTAMPTZ NOT NULL,
 raw_bytes BYTEA NOT NULL CHECK (octet_length(raw_bytes)<=131072), sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'), file_name TEXT NOT NULL,
 previous_issuance_id TEXT REFERENCES license_issuances(id), created_by TEXT NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(license_id,revision), CHECK (expires_at IS NULL OR expires_at>not_before)
);
ALTER TABLE licenses ADD CONSTRAINT licenses_current_issuance FOREIGN KEY(current_issuance_id) REFERENCES license_issuances(id) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE license_deliveries (
 id TEXT PRIMARY KEY, issuance_id TEXT NOT NULL REFERENCES license_issuances(id), operation_id TEXT NOT NULL UNIQUE,
 operation_hash TEXT NOT NULL CHECK (operation_hash ~ '^[0-9a-f]{64}$'),
 method TEXT NOT NULL CHECK (method IN ('wechat','email','usb','other')), recipient TEXT NOT NULL CHECK (btrim(recipient)<>''),
 delivered_at TIMESTAMPTZ NOT NULL, notes TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX license_deliveries_issuance ON license_deliveries(issuance_id);
CREATE TABLE offline_management_operations (
 operation_id TEXT PRIMARY KEY, operation_hash TEXT NOT NULL, kind TEXT NOT NULL, result JSONB NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE FUNCTION snail_immutable_record() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'signed originals and delivery history are append-only'; END $$;
CREATE TRIGGER license_issuances_immutable BEFORE UPDATE OR DELETE ON license_issuances FOR EACH ROW EXECUTE FUNCTION snail_immutable_record();
CREATE TRIGGER license_deliveries_immutable BEFORE UPDATE OR DELETE ON license_deliveries FOR EACH ROW EXECUTE FUNCTION snail_immutable_record();
CREATE FUNCTION snail_product_code_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.license_product_code IS DISTINCT FROM NEW.license_product_code AND EXISTS(SELECT 1 FROM licenses WHERE product_id=OLD.id AND fulfillment_mode='offline_file') THEN
   RAISE EXCEPTION 'issued product contract code is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER snail_product_code_guard BEFORE UPDATE ON products FOR EACH ROW EXECUTE FUNCTION snail_product_code_guard();
