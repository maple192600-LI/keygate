-- This task has no legacy customer data. Never automatically destroy newly issued history.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM commercial_customers) OR EXISTS(SELECT 1 FROM offline_requests) OR EXISTS(SELECT 1 FROM license_issuances) THEN
  RAISE EXCEPTION 'commercial data exists; restore a verified backup instead of destructive downgrade';
 END IF;
END $$;
DROP TRIGGER snail_product_code_guard ON products;
DROP FUNCTION snail_product_code_guard();
DROP TABLE offline_management_operations;
DROP TABLE license_deliveries;
ALTER TABLE licenses DROP CONSTRAINT licenses_current_issuance;
DROP TABLE license_issuances;
DROP FUNCTION snail_immutable_record();
DROP TABLE offline_device_bindings;
ALTER TABLE licenses DROP CONSTRAINT licenses_fulfillment_contact;
DROP INDEX offline_licenses_expiry;
DROP INDEX offline_licenses_customer;
ALTER TABLE licenses DROP COLUMN current_issuance_id;
ALTER TABLE licenses DROP COLUMN display_number;
DROP SEQUENCE snail_license_display_seq;
ALTER TABLE licenses DROP COLUMN current_revision;
ALTER TABLE licenses DROP COLUMN issuance_enabled;
ALTER TABLE licenses DROP COLUMN fulfillment_mode;
ALTER TABLE licenses DROP COLUMN customer_id;
ALTER TABLE licenses ALTER COLUMN email SET NOT NULL;
DROP TABLE offline_requests;
DROP TABLE commercial_customers;
