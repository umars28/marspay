CREATE TABLE outbox (
  id            BIGSERIAL PRIMARY KEY,
  topic         TEXT NOT NULL,
  partition_key TEXT NOT NULL,
  event_type    TEXT NOT NULL,
  payload       BYTEA NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at  TIMESTAMPTZ,
  attempts      INT NOT NULL DEFAULT 0,
  last_error    TEXT
);

CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
CREATE INDEX outbox_key_idx ON outbox (partition_key, id) WHERE published_at IS NULL;
CREATE INDEX outbox_published_idx ON outbox (published_at) WHERE published_at IS NOT NULL;
