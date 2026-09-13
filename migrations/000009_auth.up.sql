CREATE TABLE otp_challenges (
  id           TEXT PRIMARY KEY,
  phone        TEXT NOT NULL,
  code_hash    TEXT NOT NULL,
  purpose      TEXT NOT NULL DEFAULT 'login' CHECK (purpose IN ('login','step_up')),
  attempts     INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 3,
  expires_at   TIMESTAMPTZ NOT NULL,
  consumed_at  TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX otp_challenges_phone_idx ON otp_challenges (phone, created_at DESC);

CREATE TABLE sessions (
  id                 TEXT PRIMARY KEY,
  user_id            TEXT NOT NULL REFERENCES users(id),
  device_id          TEXT NOT NULL REFERENCES devices(id),
  access_hash        TEXT NOT NULL UNIQUE,
  refresh_hash       TEXT NOT NULL UNIQUE,
  access_expires_at  TIMESTAMPTZ NOT NULL,
  refresh_expires_at TIMESTAMPTZ NOT NULL,
  rotated_to         TEXT REFERENCES sessions(id),
  revoked_at         TIMESTAMPTZ,
  revoked_reason     TEXT,
  last_seen_at       TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx ON sessions (user_id, created_at DESC);
CREATE INDEX sessions_device_idx ON sessions (device_id) WHERE revoked_at IS NULL;

ALTER TABLE users ADD COLUMN pin_attempts INT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN pin_locked_until TIMESTAMPTZ;
