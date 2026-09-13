CREATE TABLE velocity_rules (
  code         TEXT PRIMARY KEY,
  description  TEXT NOT NULL,
  window_sec   INT NOT NULL CHECK (window_sec > 0),
  threshold    INT NOT NULL CHECK (threshold > 0),
  action       TEXT NOT NULL CHECK (action IN ('flag','require_otp','hold_withdrawal','freeze')),
  mode         TEXT NOT NULL DEFAULT 'monitor' CHECK (mode IN ('monitor','active','disabled')),
  updated_by   TEXT,
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE risk_alerts (
  id              TEXT PRIMARY KEY,
  subject_type    TEXT NOT NULL CHECK (subject_type IN ('user','merchant')),
  subject_id      TEXT NOT NULL,
  rule_code       TEXT NOT NULL REFERENCES velocity_rules(code),
  detail          TEXT NOT NULL,
  observed_minor  BIGINT,
  severity        TEXT NOT NULL CHECK (severity IN ('low','medium','high')),
  auto_action     TEXT,
  reviewed_by     TEXT,
  reviewed_at     TIMESTAMPTZ,
  outcome         TEXT CHECK (outcome IN ('true_positive','false_positive','inconclusive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX risk_alerts_open_idx ON risk_alerts (created_at DESC) WHERE reviewed_at IS NULL;
CREATE INDEX risk_alerts_subject_idx ON risk_alerts (subject_type, subject_id, created_at DESC);

CREATE TABLE account_blocks (
  id            TEXT PRIMARY KEY,
  subject_type  TEXT NOT NULL CHECK (subject_type IN ('user','merchant')),
  subject_id    TEXT NOT NULL,
  reason        TEXT NOT NULL,
  blocked_by    TEXT NOT NULL,
  rule_code     TEXT REFERENCES velocity_rules(code),
  held_minor    BIGINT NOT NULL DEFAULT 0,
  appeal_status TEXT CHECK (appeal_status IN ('none','submitted','accepted','rejected')),
  lifted_by     TEXT,
  lifted_at     TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX account_blocks_active_idx ON account_blocks (subject_type, subject_id)
  WHERE lifted_at IS NULL;

CREATE TABLE disputes (
  id              TEXT PRIMARY KEY,
  payment_id      TEXT NOT NULL REFERENCES payments(id),
  user_id         TEXT NOT NULL REFERENCES users(id),
  merchant_id     TEXT NOT NULL REFERENCES merchants(id),
  amount_minor    BIGINT NOT NULL CHECK (amount_minor > 0),
  reason          TEXT NOT NULL,
  covered_minor   BIGINT NOT NULL DEFAULT 0,
  loss_minor      BIGINT NOT NULL DEFAULT 0,
  status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN (
                    'open','awaiting_merchant','evidence_review','resolved_user',
                    'resolved_merchant','escalated')),
  sla_due_at      TIMESTAMPTZ NOT NULL,
  resolved_at     TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX disputes_open_idx ON disputes (sla_due_at) WHERE resolved_at IS NULL;
CREATE INDEX disputes_merchant_idx ON disputes (merchant_id, created_at DESC);

ALTER TABLE holdbacks
  ADD CONSTRAINT holdbacks_consumed_by_fkey
  FOREIGN KEY (consumed_by) REFERENCES disputes(id);

CREATE TABLE audit_log (
  id           BIGSERIAL PRIMARY KEY,
  actor        TEXT NOT NULL,
  action       TEXT NOT NULL,
  object_type  TEXT NOT NULL,
  object_id    TEXT NOT NULL,
  before_value JSONB,
  after_value  JSONB,
  reason       TEXT NOT NULL,
  ip           INET,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_object_idx ON audit_log (object_type, object_id, created_at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor, created_at DESC);

REVOKE UPDATE, DELETE ON audit_log FROM PUBLIC;
