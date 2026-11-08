-- The project's sources when a workflow version was generated; the reviewer's
-- sources_changed (statistical) and new_scope (investigation) compare against it.
ALTER TABLE workflows ADD COLUMN source_ids uuid[] NOT NULL DEFAULT '{}';
