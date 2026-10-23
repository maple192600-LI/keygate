-- No production rollback: a nullable local owner has no email to restore.
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM users WHERE email IS NULL OR username IS NOT NULL) THEN
    RAISE EXCEPTION 'local owner exists; restore a verified backup instead of destructive downgrade';
  END IF;
END $$;
ALTER TABLE products DROP COLUMN license_product_code;
DROP INDEX users_username_unique;
ALTER TABLE users DROP CONSTRAINT users_local_credentials;
ALTER TABLE users DROP COLUMN auth_version;
ALTER TABLE users DROP COLUMN password_hash;
ALTER TABLE users DROP COLUMN username;
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
