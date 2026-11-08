package workflows

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PrevText is an earlier LLM-written card, kept so that an unchanged finding
// keeps its words. Stats are the numbers it was narrated against.
type PrevText struct {
	FindingKey, Kind, Headline, Explanation string
	Caveats                                 []string
	Stats                                   map[string]any
	Plausibility                            *string
}

type InsightStore interface {
	// PreviousTexts returns LLM-narrated statistical rows for these finding
	// keys on this project, newest first.
	PreviousTexts(ctx context.Context, projectID uuid.UUID, keys []string) ([]PrevText, error)
}

type PgInsightStore struct {
	Pool *pgxpool.Pool
}

var _ InsightStore = (*PgInsightStore)(nil)

func (s *PgInsightStore) PreviousTexts(ctx context.Context, projectID uuid.UUID, keys []string) ([]PrevText, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT finding_key, kind, headline, explanation, caveats, stats, plausibility
		FROM insights
		WHERE project_id = $1 AND origin = 'statistical' AND narrated_by = 'llm' AND finding_key = ANY($2)
		ORDER BY created_at DESC LIMIT 500`, projectID, keys)
	if err != nil {
		return nil, fmt.Errorf("read earlier insight texts: %w", err)
	}
	defer rows.Close()
	var out []PrevText
	for rows.Next() {
		var p PrevText
		var caveats, stats []byte
		if err := rows.Scan(&p.FindingKey, &p.Kind, &p.Headline, &p.Explanation, &caveats, &stats, &p.Plausibility); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(caveats, &p.Caveats); err != nil {
			return nil, fmt.Errorf("caveats of %s: %w", p.FindingKey, err)
		}
		if err := json.Unmarshal(stats, &p.Stats); err != nil {
			return nil, fmt.Errorf("stats of %s: %w", p.FindingKey, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// insertInsights writes rows for one run inside the caller's transaction.
// ON CONFLICT makes a repeated finalisation harmless.
func insertInsights(ctx context.Context, tx pgx.Tx, projectID, runID uuid.UUID, rows []InsightRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		caveats, err := json.Marshal(nonNilStrings(r.Caveats))
		if err != nil {
			return err
		}
		stats := r.Stats
		if stats == nil {
			stats = map[string]any{}
		}
		statsJSON, err := json.Marshal(stats)
		if err != nil {
			return err
		}
		var chart []byte
		if len(r.ChartSpec) > 0 {
			chart = r.ChartSpec
		}
		batch.Queue(`
			INSERT INTO insights (run_id, project_id, origin, finding_key, kind, headline, explanation,
			                      caveats, stats, evidence, chart_spec, narrated_by, plausibility)
			VALUES ($1, $2, 'statistical', $3, $4, $5, $6, $7, $8, NULL, $9, $10, $11)
			ON CONFLICT (run_id, finding_key) DO NOTHING`,
			runID, projectID, r.FindingKey, r.Kind, r.Headline, r.Explanation,
			caveats, statsJSON, chart, r.NarratedBy, r.Plausibility)
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for _, r := range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert insight %s: %w", r.FindingKey, err)
		}
	}
	return nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
