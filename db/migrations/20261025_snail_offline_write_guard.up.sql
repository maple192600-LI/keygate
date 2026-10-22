-- Explicit transaction purposes separate signed file lifecycle from the native
-- online writers. No request parameter or browser header can set this purpose.
CREATE FUNCTION snail_offline_license_write_guard() RETURNS trigger LANGUAGE plpgsql AS $$
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
   IF (to_jsonb(NEW)-ARRAY['valid_from','valid_until','current_revision','current_issuance_id','status','updated_at'])
    IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['valid_from','valid_until','current_revision','current_issuance_id','status','updated_at']) THEN
    RAISE EXCEPTION 'signed issuance cannot change unrelated license fields';
   END IF;
  ELSE RAISE EXCEPTION 'native online writers cannot modify offline licenses';
  END IF;
 ELSIF NEW.fulfillment_mode='offline_file' THEN
  RAISE EXCEPTION 'online license cannot be converted to offline without signed issuance';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER snail_offline_license_write_guard BEFORE INSERT OR UPDATE OR DELETE ON licenses FOR EACH ROW EXECUTE FUNCTION snail_offline_license_write_guard();
CREATE FUNCTION snail_offline_online_record_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM licenses WHERE id=NEW.license_id AND fulfillment_mode='offline_file') THEN
  RAISE EXCEPTION 'offline licenses do not use native online lifecycle records';
 END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE name TEXT; BEGIN
 FOREACH name IN ARRAY ARRAY['activations','subscriptions','usage_events','usage_counters','floating_sessions','seats','license_addons','metered_billing','notifications','license_renewals'] LOOP
  EXECUTE format('CREATE TRIGGER snail_offline_online_record_guard BEFORE INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION snail_offline_online_record_guard()',name);
 END LOOP;
END $$;
