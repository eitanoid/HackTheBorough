-- Per-source backfill target. backfill_rows caps how many rows the initial
-- (or a re-requested) backfill loads; later polls are not capped.
-- backfill_requested_at is non-NULL while a backfill is pending and doubles as
-- a compare-and-clear token so a finishing poll never clears a newer request.
-- last_backfill stores the paging report of the most recent backfill.
-- backfill_attempts counts failed or incomplete tries of the pending
-- backfill; it is given up after a few attempts.
ALTER TABLE data_sources
  ADD COLUMN backfill_rows integer NOT NULL DEFAULT 500 CHECK (backfill_rows >= 1),
  ADD COLUMN backfill_requested_at timestamptz,
  ADD COLUMN last_backfill jsonb,
  ADD COLUMN backfill_attempts integer NOT NULL DEFAULT 0;

UPDATE data_sources SET backfill_requested_at = created_at
WHERE last_polled_at IS NULL AND poll_interval_s IS NOT NULL;

-- Rows a capped backfill deliberately left out, by external id. Sources keep
-- re-serving them (a long RSS page, a whole CSV file), so later normal polls
-- skip exactly these ids; genuinely new rows are never affected. Each
-- successful backfill replaces the set; deleting the source removes it.
CREATE TABLE backfill_skipped (
  data_source_id uuid NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
  external_id    text NOT NULL,
  PRIMARY KEY (data_source_id, external_id)
);
