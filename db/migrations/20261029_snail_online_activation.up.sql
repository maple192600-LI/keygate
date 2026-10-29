ALTER TABLE offline_requests DROP CONSTRAINT offline_requests_device_type_check;
ALTER TABLE offline_requests ADD CONSTRAINT offline_requests_device_type_check CHECK (device_type IN ('windows-machineguid-sha256/v1','windows-smbios-uuid-sha256/v1'));
ALTER TABLE offline_device_bindings DROP CONSTRAINT offline_device_bindings_device_type_check;
ALTER TABLE offline_device_bindings ADD CONSTRAINT offline_device_bindings_device_type_check CHECK (device_type IN ('windows-machineguid-sha256/v1','windows-smbios-uuid-sha256/v1'));
ALTER TABLE licenses ADD COLUMN device_limit INTEGER NOT NULL DEFAULT 1 CHECK (device_limit > 0);
ALTER TABLE licenses ADD COLUMN term_kind TEXT NOT NULL DEFAULT '' CHECK (term_kind IN ('','days','months','years','date','perpetual'));
ALTER TABLE licenses ADD COLUMN term_amount INTEGER NOT NULL DEFAULT 0 CHECK (term_amount >= 0);
ALTER TABLE licenses ADD COLUMN term_end_date DATE;
ALTER TABLE licenses ADD COLUMN term_starts_at TIMESTAMPTZ;
ALTER TABLE offline_requests ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE offline_requests ADD COLUMN build_id TEXT NOT NULL DEFAULT '';
ALTER TABLE offline_device_bindings ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE offline_device_bindings ADD COLUMN build_id TEXT NOT NULL DEFAULT '';
ALTER TABLE offline_device_bindings ADD COLUMN last_seen_at TIMESTAMPTZ;
CREATE INDEX offline_expiring_15_days ON licenses(valid_until) WHERE fulfillment_mode='offline_file' AND valid_until IS NOT NULL;
DROP INDEX offline_current_binding;
CREATE INDEX offline_current_binding ON offline_device_bindings(license_id,registered_at) WHERE superseded_at IS NULL;
CREATE UNIQUE INDEX offline_current_device_binding ON offline_device_bindings(license_id,device_type,device_digest) WHERE superseded_at IS NULL;
ALTER TABLE license_issuances DROP CONSTRAINT license_issuances_license_id_revision_key;
ALTER TABLE license_issuances DROP CONSTRAINT license_issuances_kind_check;
ALTER TABLE license_issuances ADD CONSTRAINT license_issuances_kind_check CHECK (kind IN ('initial','activate','renew','transfer'));
CREATE UNIQUE INDEX license_issuance_device_revision ON license_issuances(license_id,revision,binding_id);
CREATE OR REPLACE FUNCTION snail_offline_license_write_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE purpose TEXT := COALESCE(current_setting('snail.offline_write',true),'');
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.fulfillment_mode='offline_file' THEN RAISE EXCEPTION 'offline licenses are retained with their signed originals'; END IF;
  RETURN OLD;
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.fulfillment_mode='offline_file' AND purpose NOT IN ('issue','bundle','restore') THEN
   RAISE EXCEPTION 'offline license insertion requires the signed file lifecycle';
  END IF;
  IF NEW.fulfillment_mode='offline_file' AND NEW.user_id IS NOT NULL THEN RAISE EXCEPTION 'offline license cannot bind a login user'; END IF;
  RETURN NEW;
 END IF;
 IF OLD.fulfillment_mode='offline_file' THEN
  IF ROW(NEW.id,NEW.product_id,NEW.plan_id,NEW.customer_id,NEW.fulfillment_mode,NEW.user_id)
   IS DISTINCT FROM ROW(OLD.id,OLD.product_id,OLD.plan_id,OLD.customer_id,OLD.fulfillment_mode,OLD.user_id) THEN
   RAISE EXCEPTION 'offline contract identity is immutable';
  END IF;
  IF purpose='state' THEN
   IF (to_jsonb(NEW)-ARRAY['issuance_enabled','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['issuance_enabled','updated_at']) THEN
    RAISE EXCEPTION 'issuance state cannot change signed license terms';
   END IF;
  ELSIF purpose='issue' THEN
   IF (to_jsonb(NEW)-ARRAY['valid_from','valid_until','current_revision','current_issuance_id','status','updated_at','term_kind','term_amount','term_end_date','term_starts_at'])
    IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['valid_from','valid_until','current_revision','current_issuance_id','status','updated_at','term_kind','term_amount','term_end_date','term_starts_at']) THEN
    RAISE EXCEPTION 'signed issuance cannot change unrelated license fields';
   END IF;
  ELSE RAISE EXCEPTION 'native online writers cannot modify offline licenses';
  END IF;
 ELSIF NEW.fulfillment_mode='offline_file' THEN
  RAISE EXCEPTION 'online license cannot be converted to offline without signed issuance';
 END IF;
 RETURN NEW;
END $$;
