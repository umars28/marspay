ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'consumer'
  CHECK (role IN ('consumer','operator'));
CREATE INDEX users_operator_idx ON users (id) WHERE role = 'operator';
