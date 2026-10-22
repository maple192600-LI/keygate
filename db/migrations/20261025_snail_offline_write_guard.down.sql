DO $$ DECLARE name TEXT; BEGIN
 FOREACH name IN ARRAY ARRAY['activations','subscriptions','usage_events','usage_counters','floating_sessions','seats','license_addons','metered_billing','notifications','license_renewals'] LOOP
  EXECUTE format('DROP TRIGGER snail_offline_online_record_guard ON %I',name);
 END LOOP;
END $$;
DROP FUNCTION snail_offline_online_record_guard();
DROP TRIGGER snail_offline_license_write_guard ON licenses;
DROP FUNCTION snail_offline_license_write_guard();
