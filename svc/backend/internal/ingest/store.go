package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Source struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Config        json.RawMessage `json:"config"`
	Profile       json.RawMessage `json:"profile"`
	PollIntervalS *int            `json:"poll_interval_s"`
	LastPolledAt  *time.Time      `json:"last_polled_at"`
	LastError     *string         `json:"last_error"`
	RecordCount   int             `json:"record_count,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	// BackfillRows is the target row count for the initial (or a re-requested)
	// backfill. Later polls are not capped.
	BackfillRows int `json:"backfill_rows"`
	// BackfillRequestedAt is non-nil while a backfill is pending.
	BackfillRequestedAt *time.Time      `json:"backfill_requested_at"`
	BackfillPending     bool            `json:"backfill_pending"`
	LastBackfill        json.RawMessage `json:"last_backfill"`
	BackfillAttempts    int             `json:"backfill_attempts"`
	// HasSkipped is only filled by SourceForPoll.
	HasSkipped bool `json:"-"`
}

type Store struct {
	Pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool}
}

const sourceColumns = `id::text, name, kind, config, profile, poll_interval_s, last_polled_at, last_error, created_at,
	backfill_rows, backfill_requested_at, last_backfill, backfill_attempts`

// sourceColumnsWithCount selects sourceColumns plus the stored record count.
// It must be used with a FROM clause that aliases data_sources as s.
const sourceColumnsWithCount = `s.id::text, s.name, s.kind, s.config, s.profile, s.poll_interval_s, s.last_polled_at,
	s.last_error, s.created_at, s.backfill_rows, s.backfill_requested_at, s.last_backfill,
	s.backfill_attempts,
	(SELECT COUNT(*) FROM data_records r WHERE r.data_source_id = s.id)`

func scanSource(row pgx.Row) (Source, error) {
	return scanSourceInto(row, false)
}

func scanSourceWithCount(row pgx.Row) (Source, error) {
	return scanSourceInto(row, true)
}

func scanSourceInto(row pgx.Row, withCount bool) (Source, error) {
	var source Source
	var config, profile, lastBackfill []byte
	dest := []any{&source.ID, &source.Name, &source.Kind, &config, &profile, &source.PollIntervalS,
		&source.LastPolledAt, &source.LastError, &source.CreatedAt,
		&source.BackfillRows, &source.BackfillRequestedAt, &lastBackfill,
		&source.BackfillAttempts}
	if withCount {
		dest = append(dest, &source.RecordCount)
	}
	if err := row.Scan(dest...); err != nil {
		return source, err
	}
	source.Config = config
	source.Profile = profile
	source.LastBackfill = lastBackfill
	source.BackfillPending = source.BackfillRequestedAt != nil
	return source, nil
}

// CreateSource creates a source whose first poll backfills up to
// backfillRows rows (DefaultBackfillRows when <= 0). Sources without a poll
// interval are never polled, so no backfill is requested for them.
func (s *Store) CreateSource(ctx context.Context, name, kind string, config json.RawMessage, interval *int, backfillRows int) (Source, error) {
	if backfillRows <= 0 {
		backfillRows = DefaultBackfillRows
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO data_sources (name, kind, config, poll_interval_s, backfill_rows, backfill_requested_at)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $4::int IS NULL THEN NULL ELSE $6::timestamptz END)
		RETURNING `+sourceColumns, name, kind, config, interval, backfillRows, time.Now().UTC())
	return scanSource(row)
}

// CreateUploadedCSV creates a one-off source and inserts its parsed records in
// the same transaction so clients never observe an empty, partially-uploaded
// source. With backfillRows > 0 at most that many records are stored: the
// newest by time when the file has a time column, otherwise the last ones in
// the file (files are assumed to be appended to, matching CSV URL sources).
// With backfillRows <= 0 every row is stored, since an upload cannot be
// re-read later to recover dropped rows.
func (s *Store) CreateUploadedCSV(ctx context.Context, name, fileName string, records []RawRecord, backfillRows int) (Source, error) {
	if backfillRows > 0 {
		records = append([]RawRecord(nil), records...)
		if recordsHaveTimes(records) {
			SortNewestFirst(records)
		} else {
			reverseRecords(records)
		}
		records = CapRecords(records, backfillRows)
	} else {
		backfillRows = len(records)
		if backfillRows < 1 {
			backfillRows = 1
		}
	}
	config, err := json.Marshal(map[string]string{"file_name": fileName})
	if err != nil {
		return Source{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Source{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	source, err := scanSource(tx.QueryRow(ctx, `
		INSERT INTO data_sources (name, kind, config, poll_interval_s, backfill_rows)
		VALUES ($1, 'csv', $2, NULL, $3)
		RETURNING `+sourceColumns, name, config, backfillRows))
	if err != nil {
		return Source{}, err
	}
	inserted := 0
	for _, record := range records {
		payload, err := json.Marshal(record.Payload)
		if err != nil {
			return Source{}, fmt.Errorf("encode record %s: %w", record.ExternalID, err)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO data_records (data_source_id, external_id, observed_at, payload)
			VALUES ($1::uuid, $2, $3, $4)
			ON CONFLICT (data_source_id, external_id) DO NOTHING`,
			source.ID, record.ExternalID, record.ObservedAt, payload)
		if err != nil {
			return Source{}, err
		}
		inserted += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return Source{}, err
	}
	source.RecordCount = inserted
	return source, nil
}

func (s *Store) EnsureSource(ctx context.Context, name, kind string, config json.RawMessage, interval *int, backfillRows int) (Source, error) {
	existing, err := s.SourceByName(ctx, name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Source{}, err
	}
	return s.CreateSource(ctx, name, kind, config, interval, backfillRows)
}

func (s *Store) SourceByName(ctx context.Context, name string) (Source, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+sourceColumns+` FROM data_sources WHERE name = $1 ORDER BY created_at LIMIT 1`, name)
	return scanSource(row)
}

func (s *Store) Source(ctx context.Context, id string) (Source, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+sourceColumnsWithCount+` FROM data_sources s WHERE s.id = $1::uuid`, id)
	return scanSourceWithCount(row)
}

// SetBackfillRows changes a source's backfill target. Raising the target, or
// saving the same target while fewer rows are stored (to load more), requests
// a re-backfill (for polled sources); scheduled reports whether that happened. The stored count alone is
// not used for raises because it includes rows added by normal polls, which
// are not older history. Lowering the target never deletes rows and never
// cancels a pending backfill.
func (s *Store) SetBackfillRows(ctx context.Context, id string, rows int) (Source, bool, error) {
	if rows < 1 {
		return Source{}, false, errors.New("backfill rows must be at least 1")
	}
	var scheduled bool
	err := s.Pool.QueryRow(ctx, `
		WITH cur AS (
			SELECT s.id,
			       (s.poll_interval_s IS NOT NULL
			        AND ($2 > s.backfill_rows
			             OR ($2 = s.backfill_rows
			                 AND $2 > (SELECT COUNT(*) FROM data_records r WHERE r.data_source_id = s.id)))) AS schedule
			FROM data_sources s WHERE s.id = $1::uuid
		)
		UPDATE data_sources d SET
			backfill_rows = $2,
			backfill_requested_at = CASE WHEN cur.schedule THEN $3::timestamptz ELSE d.backfill_requested_at END,
			backfill_attempts = CASE WHEN cur.schedule THEN 0 ELSE d.backfill_attempts END
		FROM cur WHERE d.id = cur.id
		RETURNING cur.schedule`, id, rows, time.Now().UTC()).Scan(&scheduled)
	if err != nil {
		return Source{}, false, err
	}
	source, err := s.Source(ctx, id)
	return source, scheduled, err
}

// FinishBackfill clears a pending backfill request and stores its report and
// the ids its cap left out (replacing the previous skipped set), but only if
// it is still the request the finished poll served. A newer pending request is
// left untouched.
func (s *Store) FinishBackfill(ctx context.Context, id string, requestedAt time.Time, report json.RawMessage, skipped []string) error {
	if len(report) == 0 {
		report = nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Only the request this walk served is finished. If a newer request is
	// pending, its state (attempts, skipped set, report) is left alone; the
	// newer walk will write its own.
	tag, err := tx.Exec(ctx, `
		UPDATE data_sources SET
			last_backfill = COALESCE($3::jsonb, last_backfill),
			backfill_attempts = 0,
			backfill_requested_at = NULL
		WHERE id = $1::uuid AND backfill_requested_at = $2`, id, requestedAt, report)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM backfill_skipped WHERE data_source_id = $1::uuid`, id); err != nil {
		return err
	}
	if err := addSkipped(ctx, tx, id, skipped); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MaxBackfillAttempts is how many failed or incomplete tries a backfill gets
// before it is given up (the error stays visible on the source).
const MaxBackfillAttempts = 3

// FailBackfill records a failed or incomplete try of the pending backfill,
// with its report and any ids its cap left out (added to the skipped set).
// After MaxBackfillAttempts tries of the same request it is given up and
// cleared; gaveUp reports that. A try of a request that has since been
// replaced is not counted.
func (s *Store) FailBackfill(ctx context.Context, id string, requestedAt time.Time, report json.RawMessage, skipped []string) (bool, error) {
	if len(report) == 0 {
		report = nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var gaveUp bool
	err = tx.QueryRow(ctx, `
		UPDATE data_sources SET
			backfill_attempts = backfill_attempts + 1,
			last_backfill = COALESCE($3::jsonb, last_backfill),
			backfill_requested_at = CASE
				WHEN backfill_attempts + 1 >= $4 THEN NULL
				ELSE backfill_requested_at END
		WHERE id = $1::uuid AND backfill_requested_at = $2
		RETURNING backfill_requested_at IS NULL`, id, requestedAt, report, MaxBackfillAttempts).Scan(&gaveUp)
	if errors.Is(err, pgx.ErrNoRows) {
		// A newer request replaced this one; its attempts are its own.
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if err := addSkipped(ctx, tx, id, skipped); err != nil {
		return false, err
	}
	return gaveUp, tx.Commit(ctx)
}

// MaxSkippedIDs bounds the skipped set per source. Beyond it (a huge CSV
// with a small target) the oldest left-out rows may come back on later polls,
// which is safe: they are real rows, just older than the target asked for.
const MaxSkippedIDs = 100_000

// skippedLookupChunk bounds the id array sent per SkippedIDs query.
const skippedLookupChunk = 5_000

func addSkipped(ctx context.Context, tx pgx.Tx, id string, skipped []string) error {
	if len(skipped) == 0 {
		return nil
	}
	if len(skipped) > MaxSkippedIDs {
		skipped = skipped[:MaxSkippedIDs] // newest left-out rows first
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO backfill_skipped (data_source_id, external_id)
		SELECT $1::uuid, unnest($2::text[])
		ON CONFLICT DO NOTHING`, id, skipped)
	return err
}

// SkippedIDs returns which of ids a capped backfill deliberately left out.
func (s *Store) SkippedIDs(ctx context.Context, sourceID string, ids []string) (map[string]bool, error) {
	skipped := map[string]bool{}
	for start := 0; start < len(ids); start += skippedLookupChunk {
		end := min(start+skippedLookupChunk, len(ids))
		rows, err := s.Pool.Query(ctx, `
			SELECT external_id FROM backfill_skipped
			WHERE data_source_id = $1::uuid AND external_id = ANY($2)`, sourceID, ids[start:end])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			skipped[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return skipped, nil
}

// SourceForPoll reads a source without counting its records (the poll path
// never needs the count, and counting scans the source's data_records). It
// also reports whether the source has any skipped ids, so normal polls can
// skip the lookup when there are none.
func (s *Store) SourceForPoll(ctx context.Context, id string) (Source, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT `+sourceColumns+`,
		       EXISTS (SELECT 1 FROM backfill_skipped k WHERE k.data_source_id = data_sources.id)
		FROM data_sources WHERE id = $1::uuid`, id)
	var source Source
	var config, profile, lastBackfill []byte
	if err := row.Scan(&source.ID, &source.Name, &source.Kind, &config, &profile, &source.PollIntervalS,
		&source.LastPolledAt, &source.LastError, &source.CreatedAt,
		&source.BackfillRows, &source.BackfillRequestedAt, &lastBackfill,
		&source.BackfillAttempts, &source.HasSkipped); err != nil {
		return source, err
	}
	source.Config, source.Profile, source.LastBackfill = config, profile, lastBackfill
	source.BackfillPending = source.BackfillRequestedAt != nil
	return source, nil
}

// RequeueBackfill makes a pending backfill due on the next ingest tick (for
// example after an earlier attempt failed or was cancelled). It reports false
// when the source has no pending backfill or is never polled.
func (s *Store) RequeueBackfill(ctx context.Context, id string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE data_sources SET backfill_requested_at = $2, backfill_attempts = 0
		WHERE id = $1::uuid AND backfill_requested_at IS NOT NULL AND poll_interval_s IS NOT NULL`,
		id, time.Now().UTC())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkError records an error without counting it as a poll, so the source's
// schedule (and a pending backfill being due) is unchanged.
func (s *Store) MarkError(ctx context.Context, id string, pollErr error) error {
	_, err := s.Pool.Exec(ctx, `UPDATE data_sources SET last_error = $2 WHERE id = $1::uuid`, id, pollErr.Error())
	return err
}

type DataRecord struct {
	ID         int64
	ExternalID string
	ObservedAt time.Time
	Payload    map[string]any
}

func (s *Store) RecentRecords(ctx context.Context, sourceID string, limit int) ([]DataRecord, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("record limit must be from 1 to 1000")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, external_id, observed_at, payload
		FROM data_records WHERE data_source_id = $1::uuid
		ORDER BY observed_at DESC, id DESC LIMIT $2`, sourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []DataRecord
	for rows.Next() {
		var record DataRecord
		var payload []byte
		if err := rows.Scan(&record.ID, &record.ExternalID, &record.ObservedAt, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+sourceColumnsWithCount+` FROM data_sources s ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []Source
	for rows.Next() {
		source, err := scanSourceWithCount(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (s *Store) DueSources(ctx context.Context) ([]Source, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+sourceColumns+`
		FROM data_sources
		WHERE poll_interval_s IS NOT NULL
		  AND (last_polled_at IS NULL
		       OR last_polled_at + poll_interval_s * interval '1 second' <= now()
		       -- A newly requested backfill is due once, on the next tick.
		       OR (backfill_requested_at IS NOT NULL AND last_polled_at < backfill_requested_at))
		ORDER BY last_polled_at NULLS FIRST`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []Source
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (s *Store) MarkPolled(ctx context.Context, id string, polledAt time.Time, pollErr error) error {
	var errText *string
	if pollErr != nil {
		message := pollErr.Error()
		errText = &message
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE data_sources SET last_polled_at = $2, last_error = $3 WHERE id = $1::uuid`,
		id, polledAt, errText)
	return err
}

func (s *Store) InsertRecords(ctx context.Context, sourceID string, records []RawRecord) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	batch := &pgx.Batch{}
	for _, record := range records {
		payload, err := json.Marshal(record.Payload)
		if err != nil {
			return 0, fmt.Errorf("encode record %s: %w", record.ExternalID, err)
		}
		batch.Queue(`
			INSERT INTO data_records (data_source_id, external_id, observed_at, payload)
			VALUES ($1::uuid, $2, $3, $4)
			ON CONFLICT (data_source_id, external_id) DO NOTHING`,
			sourceID, record.ExternalID, record.ObservedAt, payload)
	}
	results := tx.SendBatch(ctx, batch)
	inserted := 0
	for range records {
		tag, err := results.Exec()
		if err != nil {
			results.Close()
			return 0, err
		}
		inserted += int(tag.RowsAffected())
	}
	if err := results.Close(); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inserted, nil
}
