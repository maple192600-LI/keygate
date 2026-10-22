-- Business imports may have historical actors without login accounts. Refuse
-- this downgrade rather than invent credentials or discard their provenance.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM offline_actors a LEFT JOIN users u ON u.id=a.id WHERE u.id IS NULL) THEN
   RAISE EXCEPTION 'historical actors cannot be downgraded into login accounts';
 END IF;
END $$;
ALTER TABLE offline_requests DROP CONSTRAINT offline_requests_actor;
ALTER TABLE offline_requests ADD CONSTRAINT offline_requests_created_by_fkey FOREIGN KEY(created_by) REFERENCES users(id);
ALTER TABLE license_issuances DROP CONSTRAINT license_issuances_actor;
ALTER TABLE license_issuances ADD CONSTRAINT license_issuances_created_by_fkey FOREIGN KEY(created_by) REFERENCES users(id);
ALTER TABLE license_deliveries DROP CONSTRAINT license_deliveries_actor;
ALTER TABLE license_deliveries ADD CONSTRAINT license_deliveries_created_by_fkey FOREIGN KEY(created_by) REFERENCES users(id);
ALTER TABLE offline_management_operations DROP CONSTRAINT offline_management_operations_actor;
ALTER TABLE offline_management_operations ADD CONSTRAINT offline_management_operations_created_by_fkey FOREIGN KEY(created_by) REFERENCES users(id);
DROP TABLE offline_actors;
