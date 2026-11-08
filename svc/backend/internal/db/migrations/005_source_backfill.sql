-- Per-source backfill target. backfill_rows caps how many rows the initial
-- (or a re-requested) backfill loads; later polls are not capped.
-- backfill_requested_at is non-NULL while a backfill is pending and doubles as
-- a compare-and-clear token so a finishing poll never clears a newer request.
-- last_backfill stores the paging report of the most recent backfill.
ALTER TABLE data_sources
  ADD COLUMN backfill_rows integer NOT NULL DEFAULT 500 CHECK (backfill_rows >= 1),
  ADD COLUMN backfill_requested_at timestamptz,
  ADD COLUMN last_backfill jsonb;

UPDATE data_sources SET backfill_requested_at = created_at
WHERE last_polled_at IS NULL AND poll_interval_s IS NOT NULL;
