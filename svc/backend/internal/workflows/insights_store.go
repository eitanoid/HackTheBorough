package workflows

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
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
