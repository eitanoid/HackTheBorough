// Package insightrows holds the insight card row and its insert, shared by the
// statistical run loop (workflows) and the investigator (investigate). It is
// separate because workflows imports investigate, so investigate cannot
// import workflows.
package insightrows

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	OriginStatistical   = "statistical"
	OriginInvestigation = "investigation"
)

// InsightRow is one card, ready to insert.
type InsightRow struct {
	Origin       string // statistical | investigation; empty means statistical
	FindingKey   string
	Kind         string // correlation | anomaly | custom | observation
	Headline     string
	Explanation  string
	Caveats      []string
	Stats        map[string]any  // statistical: Finding.Stats as-is; investigation: {label: value}
	Evidence     any             // investigation: [{seq, path, label, value, as_of}]; nil is SQL NULL
	ChartSpec    json.RawMessage // nil: no chart
	NarratedBy   string
	Plausibility *string // nil for template text and investigation cards
}

// Insert writes rows for one run inside the caller's transaction.
// ON CONFLICT makes a repeated finalisation harmless.
func Insert(ctx context.Context, tx pgx.Tx, projectID, runID uuid.UUID, rows []InsightRow) error {
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
		var evidence []byte
		if r.Evidence != nil {
			if evidence, err = json.Marshal(r.Evidence); err != nil {
				return err
			}
		}
		var chart []byte
		if len(r.ChartSpec) > 0 {
			chart = r.ChartSpec
		}
		origin := r.Origin
		if origin == "" {
			origin = OriginStatistical
		}
		batch.Queue(`
			INSERT INTO insights (run_id, project_id, origin, finding_key, kind, headline, explanation,
			                      caveats, stats, evidence, chart_spec, narrated_by, plausibility)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (run_id, finding_key) DO NOTHING`,
			runID, projectID, origin, r.FindingKey, r.Kind, r.Headline, r.Explanation,
			caveats, statsJSON, evidence, chart, r.NarratedBy, r.Plausibility)
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
