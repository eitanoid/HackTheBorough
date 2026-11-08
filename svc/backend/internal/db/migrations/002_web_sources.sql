ALTER TABLE data_sources
  DROP CONSTRAINT IF EXISTS data_sources_kind_check;

ALTER TABLE data_sources
  ADD CONSTRAINT data_sources_kind_check
  CHECK (kind IN ('csv', 'rss', 'web'));
