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
}

type Store struct {
	Pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool}
}

const sourceColumns = `id::text, name, kind, config, profile, poll_interval_s, last_polled_at, last_error, created_at,
	backfill_rows, backfill_requested_at, last_backfill`

// sourceColumnsWithCount selects sourceColumns plus the stored record count.
// It must be used with a FROM clause that aliases data_sources as s.
const sourceColumnsWithCount = `s.id::text, s.name, s.kind, s.config, s.profile, s.poll_interval_s, s.last_polled_at,
	s.last_error, s.created_at, s.backfill_rows, s.backfill_requested_at, s.last_backfill,
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
		&source.BackfillRows, &source.BackfillRequestedAt, &lastBackfill}
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

// CreateSource creates a source with the default backfill target.
func (s *Store) CreateSource(ctx context.Context, name, kind string, config json.RawMessage, interval *int) (Source, error) {
	return s.CreateSourceWithBackfill(ctx, name, kind, config, interval, 0)
}

// CreateSourceWithBackfill creates a source whose first poll backfills up to
// backfillRows rows (DefaultBackfillRows when <= 0). Sources without a poll
// interval are never polled, so no backfill is requested for them.
func (s *Store) CreateSourceWithBackfill(ctx context.Context, name, kind string, config json.RawMessage, interval *int, backfillRows int) (Source, error) {
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

func (s *Store) EnsureSource(ctx context.Context, name, kind string, config json.RawMessage, interval *int) (Source, error) {
	existing, err := s.SourceByName(ctx, name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Source{}, err
	}
	return s.CreateSource(ctx, name, kind, config, interval)
}

func (s *Store) SourceByName(ctx context.Context, name string) (Source, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+sourceColumns+` FROM data_sources WHERE name = $1 ORDER BY created_at LIMIT 1`, name)
	return scanSource(row)
}

func (s *Store) Source(ctx context.Context, id string) (Source, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+sourceColumnsWithCount+` FROM data_sources s WHERE s.id = $1::uuid`, id)
	return scanSourceWithCount(row)
}

// SetBackfillRows changes a source's backfill target. When the new target is
// above the number of stored rows (and the source is polled), a re-backfill is
// requested; scheduled reports whether that happened. Lowering the target
// never deletes rows and never cancels a pending backfill.
func (s *Store) SetBackfillRows(ctx context.Context, id string, rows int) (Source, bool, error) {
	if rows < 1 {
		return Source{}, false, errors.New("backfill rows must be at least 1")
	}
	var scheduled bool
	err := s.Pool.QueryRow(ctx, `
		WITH cur AS (
			SELECT s.id,
			       (s.poll_interval_s IS NOT NULL
			        AND $2 > (SELECT COUNT(*) FROM data_records r WHERE r.data_source_id = s.id)) AS schedule
			FROM data_sources s WHERE s.id = $1::uuid
		)
		UPDATE data_sources d SET
			backfill_rows = $2,
			backfill_requested_at = CASE WHEN cur.schedule THEN $3::timestamptz ELSE d.backfill_requested_at END
		FROM cur WHERE d.id = cur.id
		RETURNING cur.schedule`, id, rows, time.Now().UTC()).Scan(&scheduled)
	if err != nil {
		return Source{}, false, err
	}
	source, err := s.Source(ctx, id)
	return source, scheduled, err
}

// FinishBackfill clears a pending backfill request, but only if it is still
// the request the finished poll served (a newer request stays pending). The
// paging report is stored either way.
func (s *Store) FinishBackfill(ctx context.Context, id string, requestedAt time.Time, report json.RawMessage) error {
	if len(report) == 0 {
		report = nil
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE data_sources SET
			last_backfill = COALESCE($3::jsonb, last_backfill),
			backfill_requested_at = CASE WHEN backfill_requested_at = $2 THEN NULL ELSE backfill_requested_at END
		WHERE id = $1::uuid`, id, requestedAt, report)
	return err
}

// RequeueBackfill makes a pending backfill due on the next ingest tick (for
// example after an earlier attempt failed or was cancelled). It reports false
// when the source has no pending backfill or is never polled.
func (s *Store) RequeueBackfill(ctx context.Context, id string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE data_sources SET backfill_requested_at = $2
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

// RecordBackfillReport stores the report of a backfill that stopped early on
// an error. The request stays pending so the next scheduled poll retries it.
func (s *Store) RecordBackfillReport(ctx context.Context, id string, report json.RawMessage) error {
	_, err := s.Pool.Exec(ctx, `UPDATE data_sources SET last_backfill = $2::jsonb WHERE id = $1::uuid`, id, report)
	return err
}

// KnownExternalIDs returns which of ids are already stored for the source.
func (s *Store) KnownExternalIDs(ctx context.Context, sourceID string, ids []string) (map[string]bool, error) {
	known := map[string]bool{}
	if len(ids) == 0 {
		return known, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT external_id FROM data_records
		WHERE data_source_id = $1::uuid AND external_id = ANY($2)`, sourceID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id] = true
	}
	return known, rows.Err()
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
