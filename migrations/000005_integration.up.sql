CREATE TABLE webhook_endpoints (
  id          TEXT PRIMARY KEY,
  merchant_id TEXT NOT NULL REFERENCES merchants(id),
  url         TEXT NOT NULL,
  signing_secret TEXT NOT NULL,
  events      TEXT[] NOT NULL DEFAULT '{}',
  timeout_ms  INT NOT NULL DEFAULT 5000,
  disabled_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (merchant_id, url)
);

CREATE TABLE webhook_events (
  id           TEXT PRIMARY KEY,
  merchant_id  TEXT NOT NULL REFERENCES merchants(id),
  type         TEXT NOT NULL,
  resource_id  TEXT NOT NULL,
  payload      BYTEA NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX webhook_events_merchant_idx ON webhook_events (merchant_id, created_at DESC);

CREATE TABLE webhook_deliveries (
  id             TEXT PRIMARY KEY,
  event_id       TEXT NOT NULL REFERENCES webhook_events(id),
  endpoint_id    TEXT NOT NULL REFERENCES webhook_endpoints(id),
  attempt        INT NOT NULL DEFAULT 0,
  status         TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
                   'pending','retrying','delivered','dead_letter')),
  response_code  INT,
  response_body  TEXT,
  latency_ms     INT,
  next_retry_at  TIMESTAMPTZ,
  delivered_at   TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (event_id, endpoint_id)
);
CREATE INDEX webhook_deliveries_retry_idx ON webhook_deliveries (next_retry_at)
  WHERE status = 'retrying';
CREATE INDEX webhook_deliveries_dlq_idx ON webhook_deliveries (created_at)
  WHERE status = 'dead_letter';

CREATE TABLE provider_callbacks (
  id             TEXT PRIMARY KEY,
  provider_code  TEXT NOT NULL,
  external_ref   TEXT NOT NULL,
  event_type     TEXT NOT NULL,
  signature_ok   BOOLEAN NOT NULL,
  payload        JSONB NOT NULL,
  received_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at   TIMESTAMPTZ,
  reconciled_at  TIMESTAMPTZ,
  UNIQUE (provider_code, external_ref, event_type)
);
CREATE INDEX provider_callbacks_unreconciled_idx ON provider_callbacks (received_at)
  WHERE reconciled_at IS NULL;

CREATE TABLE reconciliation_runs (
  id               TEXT PRIMARY KEY,
  business_date    DATE NOT NULL,
  provider_code    TEXT NOT NULL,
  rows_compared    INT NOT NULL DEFAULT 0,
  rows_matched     INT NOT NULL DEFAULT 0,
  discrepancies    INT NOT NULL DEFAULT 0,
  delta_minor      BIGINT NOT NULL DEFAULT 0,
  status           TEXT NOT NULL DEFAULT 'running',
  started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at      TIMESTAMPTZ,
  UNIQUE (business_date, provider_code)
);

CREATE TABLE reconciliation_discrepancies (
  id              TEXT PRIMARY KEY,
  run_id          TEXT NOT NULL REFERENCES reconciliation_runs(id),
  external_ref    TEXT NOT NULL,
  internal_minor  BIGINT,
  provider_minor  BIGINT,
  delta_minor     BIGINT NOT NULL,
  suspected_cause TEXT,
  resolution      TEXT CHECK (resolution IN (
                    'pending','deferred','posted_manually','written_off','false_positive')),
  resolved_by     TEXT,
  resolved_at     TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX reconciliation_discrepancies_open_idx ON reconciliation_discrepancies (created_at)
  WHERE resolution IS NULL OR resolution = 'pending';
