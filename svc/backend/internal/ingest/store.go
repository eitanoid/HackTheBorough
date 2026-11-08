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
}

type Store struct {
	Pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool}
}

const sourceColumns = `id::text, name, kind, config, profile, poll_interval_s, last_polled_at, last_error, created_at`

func scanSource(row pgx.Row) (Source, error) {
	var source Source
	var config []byte
	var profile []byte
	if err := row.Scan(&source.ID, &source.Name, &source.Kind, &config, &profile, &source.PollIntervalS,
		&source.LastPolledAt, &source.LastError, &source.CreatedAt); err != nil {
		return source, err
	}
	source.Config = config
	source.Profile = profile
	return source, nil
}

func (s *Store) CreateSource(ctx context.Context, name, kind string, config json.RawMessage, interval *int) (Source, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO data_sources (name, kind, config, poll_interval_s)
		VALUES ($1, $2, $3, $4)
		RETURNING `+sourceColumns, name, kind, config, interval)
	return scanSource(row)
}

// CreateUploadedCSV creates a one-off source and inserts its parsed records in
// the same transaction so clients never observe an empty, partially-uploaded
// source.
func (s *Store) CreateUploadedCSV(ctx context.Context, name, fileName string, records []RawRecord) (Source, error) {
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
		INSERT INTO data_sources (name, kind, config, poll_interval_s)
		VALUES ($1, 'csv', $2, NULL)
		RETURNING `+sourceColumns, name, config))
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
	row := s.Pool.QueryRow(ctx, `
		SELECT s.id::text, s.name, s.kind, s.config, s.profile, s.poll_interval_s, s.last_polled_at,
		       s.last_error, s.created_at,
		       (SELECT COUNT(*) FROM data_records r WHERE r.data_source_id = s.id)
		FROM data_sources s WHERE s.id = $1::uuid`, id)
	var source Source
	var config, profile []byte
	if err := row.Scan(&source.ID, &source.Name, &source.Kind, &config, &profile, &source.PollIntervalS,
		&source.LastPolledAt, &source.LastError, &source.CreatedAt, &source.RecordCount); err != nil {
		return source, err
	}
	source.Config = config
	source.Profile = profile
	return source, nil
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
	rows, err := s.Pool.Query(ctx, `
		SELECT s.id::text, s.name, s.kind, s.config, s.profile, s.poll_interval_s, s.last_polled_at,
		       s.last_error, s.created_at,
		       (SELECT COUNT(*) FROM data_records r WHERE r.data_source_id = s.id)
		FROM data_sources s ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []Source
	for rows.Next() {
		var source Source
		var config, profile []byte
		if err := rows.Scan(&source.ID, &source.Name, &source.Kind, &config, &profile, &source.PollIntervalS,
			&source.LastPolledAt, &source.LastError, &source.CreatedAt, &source.RecordCount); err != nil {
			return nil, err
		}
		source.Config = config
		source.Profile = profile
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (s *Store) DueSources(ctx context.Context) ([]Source, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+sourceColumns+`
		FROM data_sources
		WHERE poll_interval_s IS NOT NULL
		  AND (last_polled_at IS NULL OR last_polled_at + poll_interval_s * interval '1 second' <= now())
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
