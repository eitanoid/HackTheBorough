package workflows

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/db"
)

// TestInsightsPostgres needs a database: TEST_DATABASE_URL=postgres://... go test ./internal/workflows
func TestInsightsPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	var pid uuid.UUID
	must(pool.QueryRow(ctx, `INSERT INTO projects (name, goal) VALUES ('t', 'goal') RETURNING id`).Scan(&pid))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, pid) })

	store := &PgAuthorStore{Pool: pool, Sources: &analysis.PgStore{Pool: pool}}
	doubtful, plausible := "doubtful", "plausible"
	rows := []InsightRow{
		{FindingKey: "corr:a:b", Kind: "correlation", Headline: "H1", Explanation: "E1", Caveats: []string{"reason"},
			Stats: map[string]any{"effect": -0.4}, ChartSpec: []byte(`{"mark":"line"}`), NarratedBy: "llm", Plausibility: &doubtful},
		{FindingKey: "anom:a", Kind: "anomaly", Headline: "H2", Explanation: "E2",
			Stats: map[string]any{"value": 9.0, "expected": 3.0}, NarratedBy: "template"},
		{FindingKey: "custom:a", Kind: "custom", Headline: "H3", Explanation: "E3", Caveats: nil,
			Stats: map[string]any{"effect": 0.1}, NarratedBy: "llm", Plausibility: &plausible},
	}
	_, run, err := store.SaveVersion(ctx, SaveVersion{
		ProjectID: pid, Draft: contracts.WorkflowDraft{Script: "s", InsightPrompt: "i", Rationale: "r"},
		DataUpTo: watermark, StartedAt: time.Now(), Insights: rows,
		Result: contracts.AnalysisResult{DataUpTo: watermark, Findings: []contracts.Finding{}, Rejected: []contracts.Finding{},
			Funnel: map[string]int{}, Health: contracts.Health{Sources: map[string]contracts.SourceHealth{}}},
	})
	must(err)

	// The cards are visible through latest_insights as soon as the run is.
	got := map[string]*string{}
	rs, err := pool.Query(ctx, `SELECT finding_key, plausibility FROM latest_insights WHERE project_id = $1`, pid)
	must(err)
	for rs.Next() {
		var k string
		var p *string
		must(rs.Scan(&k, &p))
		got[k] = p
	}
	rs.Close()
	if len(got) != 3 || got["corr:a:b"] == nil || *got["corr:a:b"] != "doubtful" || got["anom:a"] != nil || *got["custom:a"] != "plausible" {
		t.Fatalf("latest_insights = %v", got)
	}

	// insertInsights is idempotent per (run, finding_key).
	tx, err := pool.Begin(ctx)
	must(err)
	must(insertInsights(ctx, tx, pid, run, rows))
	must(tx.Commit(ctx))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM insights WHERE run_id = $1`, run).Scan(&n))
	if n != 3 {
		t.Fatalf("rows after a repeated insert = %d", n)
	}

	// Only LLM-narrated statistical rows are offered for reuse, with their stats.
	prev, err := (&PgInsightStore{Pool: pool}).PreviousTexts(ctx, pid, []string{"corr:a:b", "anom:a", "custom:a", "other"})
	must(err)
	if len(prev) != 2 {
		t.Fatalf("previous = %+v", prev)
	}
	byKey := map[string]PrevText{}
	for _, p := range prev {
		byKey[p.FindingKey] = p
	}
	p := byKey["corr:a:b"]
	if p.Headline != "H1" || len(p.Caveats) != 1 || p.Stats["effect"] != -0.4 || p.Plausibility == nil || *p.Plausibility != "doubtful" {
		t.Errorf("previous = %+v", p)
	}
	if c := byKey["custom:a"].Caveats; c == nil || len(c) != 0 {
		t.Errorf("empty caveats should round-trip as an empty list: %#v", c)
	}
}
