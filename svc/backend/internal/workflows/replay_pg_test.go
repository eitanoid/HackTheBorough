package workflows

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/insightrows"
	"hacktheburgh/backend/internal/testdb"
)

func TestPgRunStoreInvestigationReplay(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store := &PgRunStore{Pool: pool, Sources: &analysis.PgStore{Pool: pool}}

	var projectID, statistical, investigation uuid.UUID
	pool.QueryRow(ctx, `INSERT INTO projects (name, goal) VALUES ('p', 'g') RETURNING id`).Scan(&projectID)
	if wf, err := store.ActiveInvestigationWorkflow(ctx, projectID); err != nil || wf != nil {
		t.Fatalf("before = %v, %v", wf, err)
	}
	pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, analysis_script, insight_prompt, rationale)
		VALUES ($1, 'statistical', 1, 's', 'p', 'r') RETURNING id`, projectID).Scan(&statistical)
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, report, insight_prompt, rationale)
		VALUES ($1, 'investigation', 1, '{"rationale":"r","findings":[]}', 'b', 'r') RETURNING id`, projectID).Scan(&investigation); err != nil {
		t.Fatal(err)
	}

	wf, err := store.ActiveInvestigationWorkflow(ctx, projectID)
	if err != nil || wf == nil || wf.ID != investigation || wf.ProjectID != projectID {
		t.Fatalf("ActiveInvestigationWorkflow = %+v, %v (must not return the statistical one)", wf, err)
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	runID, err := store.StartRun(ctx, investigation, at, at)
	if err != nil {
		t.Fatal(err)
	}
	rows := []InsightRow{{
		Origin: insightrows.OriginInvestigation, FindingKey: "inv:v1:0", Kind: "observation", Headline: "H", Explanation: "E",
		Stats: map[string]any{"rho": 0.5}, Evidence: []map[string]any{{"seq": 1, "path": "result.rho", "label": "rho", "value": 0.5}},
		NarratedBy: "llm",
	}}
	result := contracts.ReplayResult{DataUpTo: at, Steps: []contracts.StepOutput{}}
	if err := store.SucceedReplay(ctx, projectID, runID, result, rows, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.SucceedReplay(ctx, projectID, runID, result, rows, time.Now().UTC()); err == nil {
		t.Fatal("finishing a run twice must fail")
	}

	var origin string
	var hasEvidence bool
	if err := pool.QueryRow(ctx, `SELECT origin, evidence IS NOT NULL FROM latest_insights WHERE project_id = $1`, projectID).Scan(&origin, &hasEvidence); err != nil {
		t.Fatal(err)
	}
	if origin != "investigation" || !hasEvidence {
		t.Fatalf("origin=%q evidence=%v", origin, hasEvidence)
	}
	if st, err := store.State(ctx, investigation); err != nil || st.SuccessfulDataUpTo == nil || !st.SuccessfulDataUpTo.Equal(at) {
		t.Fatalf("State = %+v, %v", st, err)
	}
}
