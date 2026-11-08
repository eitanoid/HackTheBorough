package investigate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/insightrows"
	"hacktheburgh/backend/internal/testdb"
)

// Needs TEST_DATABASE_URL (a throwaway database: the tables are truncated).
func TestPgStoreSaveVersionAndReload(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store := &PgStore{Pool: pool, Sources: &analysis.PgStore{Pool: pool}}

	var project uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, goal, last_error) VALUES ('p', 'goal', $1) RETURNING id`,
		ErrorPrefix+"earlier failure").Scan(&project); err != nil {
		t.Fatal(err)
	}
	if has, err := store.HasInvestigation(ctx, project); err != nil || has {
		t.Fatalf("HasInvestigation before = %v, %v", has, err)
	}

	steps := []Step{
		{Seq: 1, Tool: ToolDescribeSource, Input: raw(`{"source":"weather"}`), Output: raw(`{"key":"weather","records":2}`)},
		pyStep(2, "r = INPUT\nresult = {'rho': 0.5}", `{"rho":0.5}`),
	}
	report := contracts.InvestigationReport{Rationale: "tried", Findings: []contracts.InvestigationFinding{{
		Headline: "H", Explanation: "E", Caveats: []string{}, Claims: []contracts.Claim{{Seq: 2, Path: "result.rho", Label: "rho"}},
	}}}
	replay := FirstRun(report, steps, watermark)
	save := func() Saved {
		t.Helper()
		s, err := store.SaveVersion(ctx, SaveVersion{
			ProjectID: project, Report: report, Brief: "brief", Steps: steps, DataUpTo: watermark, StartedAt: time.Now(),
			Replay: replay, Cards: func(v int) []insightrows.InsightRow { return Cards(report, v, replay, nil, nil) },
		})
		if err != nil {
			t.Fatalf("SaveVersion: %v", err)
		}
		return s
	}

	first := save()
	if first.Version != 1 {
		t.Fatalf("version = %d", first.Version)
	}
	var runs, succeeded, cards, nsteps int
	pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'succeeded') FROM runs WHERE workflow_id = $1`, first.WorkflowID).Scan(&runs, &succeeded)
	pool.QueryRow(ctx, `SELECT count(*) FROM insights WHERE run_id = $1 AND origin = 'investigation' AND kind = 'observation' AND evidence IS NOT NULL`, first.RunID).Scan(&cards)
	pool.QueryRow(ctx, `SELECT count(*) FROM workflow_steps WHERE workflow_id = $1`, first.WorkflowID).Scan(&nsteps)
	if runs != 1 || succeeded != 1 || cards != 1 || nsteps != 2 {
		t.Fatalf("runs=%d succeeded=%d cards=%d steps=%d, want 1/1/1/2", runs, succeeded, cards, nsteps)
	}
	var lastErr *string
	pool.QueryRow(ctx, `SELECT last_error FROM projects WHERE id = $1`, project).Scan(&lastErr)
	if lastErr != nil {
		t.Fatalf("our own earlier error was not cleared: %q", *lastErr)
	}

	// Another component's error survives a save.
	pool.Exec(ctx, `UPDATE projects SET last_error = 'author output invalid' WHERE id = $1`, project)
	second := save()
	pool.QueryRow(ctx, `SELECT last_error FROM projects WHERE id = $1`, project).Scan(&lastErr)
	if second.Version != 2 || lastErr == nil || *lastErr != "author output invalid" {
		t.Fatalf("version=%d last_error=%v", second.Version, lastErr)
	}
	var active int
	var activeID uuid.UUID
	pool.QueryRow(ctx, `SELECT count(*), max(id::text)::uuid FROM workflows WHERE project_id = $1 AND kind = 'investigation' AND is_active`, project).Scan(&active, &activeID)
	if active != 1 || activeID != second.WorkflowID {
		t.Fatalf("active = %d (%v), want only the second version", active, activeID)
	}
	if has, _ := store.HasInvestigation(ctx, project); !has {
		t.Fatal("HasInvestigation after save = false")
	}

	// Reload what the replayer needs.
	wf, err := store.LoadWorkflow(ctx, second.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Version != 2 || wf.ProjectID != project || len(wf.Steps) != 2 || wf.Steps[1].Seq != 2 || len(wf.Report.Findings) != 1 {
		t.Fatalf("workflow = %+v", wf)
	}
	if !jsonEqual(t, wf.Steps[1].Output, steps[1].Output) {
		t.Fatalf("step output changed in storage:\n got %s\nwant %s", wf.Steps[1].Output, steps[1].Output)
	}
	prev, err := store.PreviousReplay(ctx, second.WorkflowID)
	if err != nil || prev == nil || len(prev.Steps) != 1 || !prev.DataUpTo.Equal(watermark) {
		t.Fatalf("PreviousReplay = %+v, %v", prev, err)
	}
	var evidence []Evidence
	var raw []byte
	pool.QueryRow(ctx, `SELECT evidence FROM insights WHERE run_id = $1`, second.RunID).Scan(&raw)
	if err := json.Unmarshal(raw, &evidence); err != nil || len(evidence) != 1 || evidence[0].Label != "rho" || evidence[0].Value != 0.5 {
		t.Fatalf("evidence = %s (%v)", raw, err)
	}
}

func TestPgStoreLoadInputsLeads(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store := &PgStore{Pool: pool, Sources: &analysis.PgStore{Pool: pool}}

	var project, wf uuid.UUID
	pool.QueryRow(ctx, `INSERT INTO projects (name, goal) VALUES ('p', 'does rain move trips?') RETURNING id`).Scan(&project)
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, analysis_script, insight_prompt, rationale)
		VALUES ($1, 'statistical', 1, 's', 'care about trips', 'r') RETURNING id`, project).Scan(&wf); err != nil {
		t.Fatal(err)
	}
	unit := "mm"
	res := contracts.AnalysisResult{DataUpTo: watermark,
		Health: contracts.Health{Series: []contracts.SeriesInfo{{ID: "rain", Label: "Rain", Unit: &unit}, {ID: "trips", Label: "Trips"}}},
		Findings: []contracts.Finding{{FindingKey: "corr:rain:trips:lag+1", Kind: "correlation", Series: []string{"rain", "trips"},
			Stats: map[string]any{"effect": -0.4, "q": 0.02, "lag": 1.0}}}}
	b, _ := json.Marshal(res)
	pool.Exec(ctx, `INSERT INTO runs (workflow_id, data_up_to, status, result, finished_at) VALUES ($1, $2, 'succeeded', $3, now())`, wf, watermark, b)

	in, err := store.LoadInputs(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if in.Goal != "does rain move trips?" || in.InsightPrompt != "care about trips" || len(in.Leads) != 1 {
		t.Fatalf("inputs = %+v", in)
	}
	l := in.Leads[0]
	if l.Key != "corr:rain:trips:lag+1" || l.Direction != "negative" || !l.HasQ || l.Q != 0.02 ||
		len(l.Series) != 2 || l.Series[0] != "Rain (mm)" || l.Series[1] != "Trips" {
		t.Fatalf("lead = %+v", l)
	}
}

// jsonEqual compares JSON values, not bytes: jsonb rewrites key order and
// whitespace, so stored step outputs are equal to the recorded ones only
// semantically.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
