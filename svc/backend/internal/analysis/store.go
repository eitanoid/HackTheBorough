package analysis

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"hacktheburgh/backend/internal/contracts"
)

// Store reads what the analysis needs.
type Store interface {
	ProjectSources(ctx context.Context, projectID uuid.UUID) ([]SourceMeta, error)
	Records(ctx context.Context, sourceIDs []uuid.UUID, from, to time.Time) (map[uuid.UUID][]Record, error)
	MaxObservedAt(ctx context.Context, projectID uuid.UUID) (*time.Time, error)
}

// PgStore is the Postgres implementation: plain SQL over project_sources,
// data_sources and data_records.
type PgStore struct{ Pool *pgxpool.Pool }

var _ Store = (*PgStore)(nil)

func (s *PgStore) ProjectSources(ctx context.Context, projectID uuid.UUID) ([]SourceMeta, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, d.name, d.kind, d.profile, d.created_at
		FROM project_sources ps
		JOIN data_sources d ON d.id = ps.data_source_id
		WHERE ps.project_id = $1
		ORDER BY d.created_at, d.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceMeta
	for rows.Next() {
		var m SourceMeta
		var profile *contracts.SourceProfile // NULL until the profiler has run
		if err := rows.Scan(&m.ID, &m.Name, &m.Kind, &profile, &m.CreatedAt); err != nil {
			return nil, err
		}
		if profile != nil {
			m.Profile = *profile
			m.Summary = profile.Summary
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PgStore) Records(ctx context.Context, sourceIDs []uuid.UUID, from, to time.Time) (map[uuid.UUID][]Record, error) {
	out := make(map[uuid.UUID][]Record, len(sourceIDs))
	if len(sourceIDs) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT data_source_id, external_id, observed_at, payload
		FROM data_records
		WHERE data_source_id = ANY($1) AND observed_at >= $2 AND observed_at <= $3
		ORDER BY data_source_id, observed_at, external_id`, sourceIDs, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var r Record
		if err := rows.Scan(&id, &r.ExternalID, &r.ObservedAt, &r.Payload); err != nil {
			return nil, err
		}
		out[id] = append(out[id], r)
	}
	return out, rows.Err()
}

func (s *PgStore) MaxObservedAt(ctx context.Context, projectID uuid.UUID) (*time.Time, error) {
	var t *time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT max(r.observed_at)
		FROM project_sources ps
		JOIN data_records r ON r.data_source_id = ps.data_source_id
		WHERE ps.project_id = $1`, projectID).Scan(&t)
	return t, err
}
