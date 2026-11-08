package workflows

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/testdb"
)

func TestPgRunStoreSchedulingAndPersistence(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	var projectID, sourceID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (name, goal) VALUES ('Runner test', 'Test scheduling') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO data_sources (name, kind, config) VALUES ('Runner source', 'csv', '{}'::jsonb) RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_sources (project_id, data_source_id) VALUES ($1, $2)`, projectID, sourceID); err != nil {
		t.Fatal(err)
	}
	watermark := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO data_records (data_source_id, external_id, observed_at, payload)
		VALUES ($1, 'row-1', $2, '{}'::jsonb)`, sourceID, watermark); err != nil {
		t.Fatal(err)
	}
	var workflowID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, analysis_script, insight_prompt, rationale)
		VALUES ($1, 'statistical', 1, 'result = {}', 'Narrate results', 'Test workflow') RETURNING id`, projectID).Scan(&workflowID); err != nil {
		t.Fatal(err)
	}

	store := &PgRunStore{Pool: pool, Sources: &analysis.PgStore{Pool: pool}}
	projects, err := store.ProjectIDs(ctx)
	if err != nil || len(projects) != 1 || projects[0] != projectID {
		t.Fatalf("ProjectIDs = %v, err=%v", projects, err)
	}
	linked, err := store.ProjectsForSource(ctx, sourceID)
	if err != nil || len(linked) != 1 || linked[0] != projectID {
		t.Fatalf("ProjectsForSource = %v, err=%v", linked, err)
	}
	gotWatermark, hasData, err := store.Watermark(ctx, projectID)
	if err != nil || !hasData || !gotWatermark.Equal(watermark) {
		t.Fatalf("Watermark = %v, hasData=%v, err=%v", gotWatermark, hasData, err)
	}
	workflow, err := store.ActiveStatisticalWorkflow(ctx, projectID)
	if err != nil || workflow == nil || workflow.ID != workflowID {
		t.Fatalf("ActiveStatisticalWorkflow = %#v, err=%v", workflow, err)
	}
	projectContext, err := store.ProjectContext(ctx, projectID)
	if err != nil || projectContext.Goal != "Test scheduling" || len(projectContext.Sources) != 1 {
		t.Fatalf("ProjectContext = %#v, err=%v", projectContext, err)
	}

	state, err := store.State(ctx, workflowID)
	if err != nil || state.SuccessfulDataUpTo != nil || state.LastStatus != "" {
		t.Fatalf("initial state = %#v, err=%v", state, err)
	}
	runID, err := store.StartRun(ctx, workflowID, watermark, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	result := contracts.AnalysisResult{DataUpTo: watermark, Funnel: map[string]int{"tests_run": 0}}
	if err := store.SucceedRun(ctx, projectID, runID, result, nil, time.Now().UTC()); err != nil {
		t.Fatalf("SucceedRun: %v", err)
	}
	state, err = store.State(ctx, workflowID)
	if err != nil || state.LastStatus != "succeeded" || state.SuccessfulDataUpTo == nil || !state.SuccessfulDataUpTo.Equal(watermark) {
		t.Fatalf("successful state = %#v, err=%v", state, err)
	}

	failedRun, err := store.StartRun(ctx, workflowID, watermark.Add(time.Minute), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailRun(ctx, failedRun, "temporary failure", time.Now().UTC()); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	state, err = store.State(ctx, workflowID)
	if err != nil || state.LastStatus != "failed" || state.ConsecutiveFailures != 1 || state.SuccessfulDataUpTo == nil {
		t.Fatalf("failed state = %#v, err=%v", state, err)
	}
}
