-- Historical actors are business provenance, never imported login accounts.
CREATE TABLE offline_actors (
 id TEXT PRIMARY KEY,
 display_name TEXT NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO offline_actors(id,display_name)
 SELECT id,COALESCE(NULLIF(username,''),NULLIF(name,''),'历史操作者') FROM users
 WHERE id IN (SELECT created_by FROM offline_requests UNION SELECT created_by FROM license_issuances
 UNION SELECT created_by FROM license_deliveries UNION SELECT created_by FROM offline_management_operations);
ALTER TABLE offline_requests DROP CONSTRAINT offline_requests_created_by_fkey;
ALTER TABLE offline_requests ADD CONSTRAINT offline_requests_actor FOREIGN KEY(created_by) REFERENCES offline_actors(id);
ALTER TABLE license_issuances DROP CONSTRAINT license_issuances_created_by_fkey;
ALTER TABLE license_issuances ADD CONSTRAINT license_issuances_actor FOREIGN KEY(created_by) REFERENCES offline_actors(id);
ALTER TABLE license_deliveries DROP CONSTRAINT license_deliveries_created_by_fkey;
ALTER TABLE license_deliveries ADD CONSTRAINT license_deliveries_actor FOREIGN KEY(created_by) REFERENCES offline_actors(id);
ALTER TABLE offline_management_operations DROP CONSTRAINT offline_management_operations_created_by_fkey;
ALTER TABLE offline_management_operations ADD CONSTRAINT offline_management_operations_actor FOREIGN KEY(created_by) REFERENCES offline_actors(id);
