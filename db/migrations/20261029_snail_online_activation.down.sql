DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM offline_requests WHERE device_type='windows-smbios-uuid-sha256/v1') OR EXISTS(SELECT 1 FROM offline_device_bindings WHERE device_type='windows-smbios-uuid-sha256/v1') THEN
  RAISE EXCEPTION 'SMBIOS-bound authorization records must be moved before rolling back the contract';
 END IF;
 IF EXISTS(SELECT 1 FROM offline_device_bindings WHERE superseded_at IS NULL GROUP BY license_id HAVING count(*)>1) THEN
  RAISE EXCEPTION 'licenses with multiple current devices must be reduced before rolling back the contract';
 END IF;
 IF EXISTS(SELECT 1 FROM license_issuances GROUP BY license_id,revision HAVING count(*)>1) THEN
  RAISE EXCEPTION 'licenses with multiple signed devices at one revision must be resolved before rolling back the contract';
 END IF;
 IF EXISTS(SELECT 1 FROM license_issuances WHERE kind='activate') THEN
  RAISE EXCEPTION 'device activation issuance history must be migrated before rolling back the contract';
 END IF;
 IF EXISTS(SELECT 1 FROM licenses WHERE fulfillment_mode='offline_file' AND (device_limit<>1 OR term_kind<>'' OR term_amount<>0 OR term_end_date IS NOT NULL OR term_starts_at IS NOT NULL)) THEN
  RAISE EXCEPTION 'online activation license terms must be migrated before rolling back the contract';
 END IF;
END $$;
ALTER TABLE offline_device_bindings DROP CONSTRAINT offline_device_bindings_device_type_check;
ALTER TABLE offline_device_bindings ADD CONSTRAINT offline_device_bindings_device_type_check CHECK (device_type='windows-machineguid-sha256/v1');
ALTER TABLE offline_requests DROP CONSTRAINT offline_requests_device_type_check;
ALTER TABLE offline_requests ADD CONSTRAINT offline_requests_device_type_check CHECK (device_type='windows-machineguid-sha256/v1');
CREATE OR REPLACE FUNCTION snail_offline_license_write_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE purpose TEXT := COALESCE(current_setting('snail.offline_write',true),'');
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.fulfillment_mode='offline_file' THEN RAISE EXCEPTION 'offline licenses are retained with their signed originals'; END IF;
  RETURN OLD;
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.fulfillment_mode='offline_file' AND purpose NOT IN ('issue','bundle','restore') THEN RAISE EXCEPTION 'offline license insertion requires the signed file lifecycle'; END IF;
  IF NEW.fulfillment_mode='offline_file' AND NEW.user_id IS NOT NULL THEN RAISE EXCEPTION 'offline license cannot bind a login user'; END IF;
  RETURN NEW;
 END IF;
 IF OLD.fulfillment_mode='offline_file' THEN
  IF ROW(NEW.id,NEW.product_id,NEW.plan_id,NEW.customer_id,NEW.fulfillment_mode,NEW.user_id) IS DISTINCT FROM ROW(OLD.id,OLD.product_id,OLD.plan_id,OLD.customer_id,OLD.fulfillment_mode,OLD.user_id) THEN RAISE EXCEPTION 'offline contract identity is immutable'; END IF;
  IF purpose='state' THEN
   IF (to_jsonb(NEW)-ARRAY['issuance_enabled','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['issuance_enabled','updated_at']) THEN RAISE EXCEPTION 'issuance state cannot change signed license terms'; END IF;
  ELSIF purpose='issue' THEN
   IF (to_jsonb(NEW)-ARRAY['valid_from','valid_until','current_revision','current_issuance_id','status','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['valid_from','valid_until','current_revision','current_issuance_id','status','updated_at']) THEN RAISE EXCEPTION 'signed issuance cannot change unrelated license fields'; END IF;
  ELSE RAISE EXCEPTION 'native online writers cannot modify offline licenses'; END IF;
 ELSIF NEW.fulfillment_mode='offline_file' THEN RAISE EXCEPTION 'online license cannot be converted to offline without signed issuance'; END IF;
 RETURN NEW;
END $$;
DROP INDEX offline_expiring_15_days;
DROP INDEX license_issuance_device_revision;
ALTER TABLE license_issuances DROP CONSTRAINT license_issuances_kind_check;
ALTER TABLE license_issuances ADD CONSTRAINT license_issuances_kind_check CHECK (kind IN ('initial','renew','transfer'));
ALTER TABLE license_issuances ADD CONSTRAINT license_issuances_license_id_revision_key UNIQUE (license_id,revision);
DROP INDEX offline_current_device_binding;
DROP INDEX offline_current_binding;
CREATE UNIQUE INDEX offline_current_binding ON offline_device_bindings(license_id) WHERE superseded_at IS NULL;
ALTER TABLE offline_device_bindings DROP COLUMN last_seen_at;
ALTER TABLE offline_device_bindings DROP COLUMN build_id;
ALTER TABLE offline_device_bindings DROP COLUMN app_version;
ALTER TABLE offline_requests DROP COLUMN build_id;
ALTER TABLE offline_requests DROP COLUMN app_version;
ALTER TABLE licenses DROP COLUMN device_limit;
ALTER TABLE licenses DROP COLUMN term_kind;
ALTER TABLE licenses DROP COLUMN term_amount;
ALTER TABLE licenses DROP COLUMN term_end_date;
ALTER TABLE licenses DROP COLUMN term_starts_at;
