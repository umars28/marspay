ALTER TABLE users DROP COLUMN IF EXISTS pin_locked_until;
ALTER TABLE users DROP COLUMN IF EXISTS pin_attempts;

DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS otp_challenges;
