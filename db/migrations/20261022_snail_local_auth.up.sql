ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ADD COLUMN username TEXT;
ALTER TABLE users ADD COLUMN password_hash TEXT;
ALTER TABLE users ADD COLUMN auth_version BIGINT NOT NULL DEFAULT 1 CHECK (auth_version > 0);
ALTER TABLE users ADD CONSTRAINT users_local_credentials CHECK (
  (username IS NULL AND password_hash IS NULL) OR
  (username IS NOT NULL AND password_hash IS NOT NULL AND username ~ '^[a-z0-9_-]{3,32}$')
);
CREATE UNIQUE INDEX users_username_unique ON users (username) WHERE username IS NOT NULL;
ALTER TABLE products ADD COLUMN license_product_code TEXT UNIQUE;
