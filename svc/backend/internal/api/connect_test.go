package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	v1 "hacktheburgh/backend/gen/hacktheburgh/v1"
	v1connect "hacktheburgh/backend/gen/hacktheburgh/v1/hacktheburghv1connect"
	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/testdb"
	"hacktheburgh/backend/internal/workflows"
)

func TestConnectSourceConfigurationMapsWebAdapterFields(t *testing.T) {
	schema, err := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{"title": map[string]any{"type": "string"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	newestFirst := false
	kind, raw, err := sourceConfig(v1.SourceKind_SOURCE_KIND_WEB, &v1.SourceConfig{
		Url:    "https://example.com/data",
		Query:  "Extract daily records",
		Schema: schema,
		Pagination: &v1.WebPagination{
			Strategy: v1.PaginationStrategy_PAGINATION_STRATEGY_SCROLL,
			MaxPages: 3, MaxScrolls: 8, ScrollDelaySeconds: 0.75,
			Selector: ".results", NewestFirst: &newestFirst,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "web" {
		t.Fatalf("kind = %q, want web", kind)
	}
	parsed, err := ingest.ParseWebConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query != "Extract daily records" || parsed.Pagination == nil || parsed.Pagination.Strategy != "scroll" || parsed.Pagination.ScrollDelay != 0.75 || parsed.Pagination.NewestFirst == nil || *parsed.Pagination.NewestFirst {
		t.Fatalf("parsed config = %#v", parsed)
	}
}

func TestConnectSourceErrors(t *testing.T) {
	t.Run("invalid source config", func(t *testing.T) {
		svc := &ConnectService{Store: &ingest.Store{}}
		_, err := svc.CreateSource(context.Background(), &v1.CreateSourceRequest{
			Name: "Feed", Kind: v1.SourceKind_SOURCE_KIND_RSS,
			Config: &v1.SourceConfig{Url: "not a url"},
		})
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Fatalf("code = %v, want InvalidArgument; err=%v", got, err)
		}
	})

	t.Run("unavailable store", func(t *testing.T) {
		svc := &ConnectService{}
		_, err := svc.ListSources(context.Background(), &v1.ListSourcesRequest{})
		if got := connect.CodeOf(err); got != connect.CodeUnavailable {
			t.Fatalf("code = %v, want Unavailable; err=%v", got, err)
		}
	})
}

func TestConnectCreateProjectValidation(t *testing.T) {
	svc := &ConnectService{Store: &ingest.Store{}}
	tests := []struct {
		name string
		req  *v1.CreateProjectRequest
	}{
		{name: "missing name", req: &v1.CreateProjectRequest{Goal: "Explore data", SourceIds: []string{"id"}}},
		{name: "missing goal", req: &v1.CreateProjectRequest{Name: "Project", SourceIds: []string{"id"}}},
		{name: "missing sources", req: &v1.CreateProjectRequest{Name: "Project", Goal: "Explore data"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.CreateProject(context.Background(), test.req)
			if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, want InvalidArgument; err=%v", got, err)
			}
		})
	}
}

func TestConnectHTTPRoutes(t *testing.T) {
	t.Run("unavailable dependency", func(t *testing.T) {
		httpServer := httptest.NewServer((&Server{}).Routes())
		t.Cleanup(httpServer.Close)
		client := connectTestClient(t, httpServer)
		_, err := client.ListSources(context.Background(), &v1.ListSourcesRequest{})
		if got := connect.CodeOf(err); got != connect.CodeUnavailable {
			t.Fatalf("code = %v, want Unavailable; err=%v", got, err)
		}
	})

	t.Run("invalid source config", func(t *testing.T) {
		httpServer := httptest.NewServer((&Server{Store: &ingest.Store{}}).Routes())
		t.Cleanup(httpServer.Close)
		client := connectTestClient(t, httpServer)
		_, err := client.CreateSource(context.Background(), &v1.CreateSourceRequest{
			Name: "Bad feed", Kind: v1.SourceKind_SOURCE_KIND_RSS,
			Config: &v1.SourceConfig{Url: "not a URL"},
		})
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Fatalf("code = %v, want InvalidArgument; err=%v", got, err)
		}
	})
}

func connectTestClient(t *testing.T, server *httptest.Server) v1connect.AnduinServiceClient {
	t.Helper()
	transport := connecthttp.NewTransport(server.Client(), server.URL)
	return v1connect.NewAnduinServiceClient(connect.NewClient(transport))
}

func TestConnectSourceRPCsIntegration(t *testing.T) {
	store := ingest.NewStore(testdb.Open(t))
	httpServer := httptest.NewServer((&Server{Store: store}).Routes())
	t.Cleanup(httpServer.Close)
	client := connectTestClient(t, httpServer)
	ctx := context.Background()

	created, err := client.CreateSource(ctx, &v1.CreateSourceRequest{
		Name: "Example feed", Kind: v1.SourceKind_SOURCE_KIND_RSS,
		Config: &v1.SourceConfig{Url: "https://example.com/feed.xml"},
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if created.GetSource().GetId() == "" || created.GetSource().GetKind() != v1.SourceKind_SOURCE_KIND_RSS || created.GetSource().GetPollIntervalSeconds() != 300 {
		t.Fatalf("created source = %#v", created.GetSource())
	}

	listed, err := client.ListSources(ctx, &v1.ListSourcesRequest{})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(listed.GetSources()) != 1 || listed.GetSources()[0].GetId() != created.GetSource().GetId() {
		t.Fatalf("listed sources = %#v", listed.GetSources())
	}

	detailed, err := client.GetSource(ctx, &v1.GetSourceRequest{Id: created.GetSource().GetId()})
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if detailed.GetSource().GetId() != created.GetSource().GetId() || len(detailed.GetRecentRecords()) != 0 {
		t.Fatalf("source detail = %#v", detailed)
	}

	uploaded, err := client.UploadCSVSource(ctx, &v1.UploadCSVSourceRequest{
		Name: "CSV upload", FileName: "rows.csv", CsvData: []byte("date,value\n2026-01-01,4\n2026-01-02,7\n"),
	})
	if err != nil {
		t.Fatalf("UploadCSVSource: %v", err)
	}
	if uploaded.GetSource().GetKind() != v1.SourceKind_SOURCE_KIND_CSV || uploaded.GetSource().GetRecordCount() != 2 || uploaded.GetSource().PollIntervalSeconds != nil {
		t.Fatalf("uploaded source = %#v", uploaded.GetSource())
	}
	detailed, err = client.GetSource(ctx, &v1.GetSourceRequest{Id: uploaded.GetSource().GetId()})
	if err != nil {
		t.Fatalf("GetSource uploaded CSV: %v", err)
	}
	if len(detailed.GetRecentRecords()) != 2 {
		t.Fatalf("recent records = %#v, want 2", detailed.GetRecentRecords())
	}
}

func TestConnectProjectReadRPCsIntegration(t *testing.T) {
	pool := testdb.Open(t)
	store := ingest.NewStore(pool)
	ctx := context.Background()
	source, err := store.CreateSource(ctx, "Project source", "rss", []byte(`{"url":"https://example.com/feed.xml"}`), intPtr(300))
	if err != nil {
		t.Fatal(err)
	}
	var projectID string
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, goal) VALUES ('Project', 'Study the data') RETURNING id::text`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_sources (project_id, data_source_id) VALUES ($1::uuid, $2::uuid)`, projectID, source.ID); err != nil {
		t.Fatal(err)
	}
	var statisticalWorkflowID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, analysis_script, insight_prompt, rationale)
		VALUES ($1::uuid, 'statistical', 1, 'print(1)', 'Prompt', 'Reason') RETURNING id::text`, projectID).Scan(&statisticalWorkflowID); err != nil {
		t.Fatal(err)
	}
	var investigationWorkflowID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, report, insight_prompt, rationale)
		VALUES ($1::uuid, 'investigation', 1, '{}'::jsonb, 'Prompt', 'Reason') RETURNING id::text`, projectID).Scan(&investigationWorkflowID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_steps (workflow_id, seq, tool, input, output)
		VALUES ($1::uuid, 1, 'describe_source', '{}'::jsonb, '{"records": 2}'::jsonb)`, investigationWorkflowID); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{"funnel": map[string]int{
		"tests_run": 10, "raw_p_lt_05": 5, "passed_fdr": 2, "passed_stability": 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (workflow_id, data_up_to, status, result, finished_at)
		VALUES ($1::uuid, $2, 'succeeded', $3::jsonb, $2) RETURNING id::text`, statisticalWorkflowID, time.Now().UTC(), result).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO insights (run_id, project_id, origin, finding_key, kind, headline, explanation, caveats, stats, narrated_by, plausibility)
		VALUES ($1::uuid, $2::uuid, 'statistical', 'finding-1', 'correlation', 'Headline', 'Explanation', '["caveat"]'::jsonb, '{"rho": 0.5}'::jsonb, 'llm', 'doubtful'),
		       ($1::uuid, $2::uuid, 'statistical', 'finding-2', 'anomaly', 'Headline 2', 'Explanation 2', '[]'::jsonb, '{"rho": 0.6}'::jsonb, 'template', NULL)`, runID, projectID); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer((&Server{Store: store}).Routes())
	t.Cleanup(httpServer.Close)
	client := connectTestClient(t, httpServer)

	projects, err := client.ListProjects(ctx, &v1.ListProjectsRequest{})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects.GetProjects()) != 1 || projects.GetProjects()[0].GetLatestRunStatus() != v1.RunStatus_RUN_STATUS_SUCCEEDED || projects.GetProjects()[0].GetFindingCount() != 2 || len(projects.GetProjects()[0].GetProject().GetSourceIds()) != 1 {
		t.Fatalf("projects = %#v", projects.GetProjects())
	}

	project, err := client.GetProject(ctx, &v1.GetProjectRequest{Id: projectID})
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if len(project.GetActiveWorkflows()) != 2 || project.GetLatestRun().GetId() == "" || project.GetFunnel().GetTestsRun() != 10 || project.GetFunnel().GetRawPLt_05() != 5 {
		t.Fatalf("project = %#v", project)
	}

	insights, err := client.ListInsights(ctx, &v1.ListInsightsRequest{ProjectId: projectID})
	if err != nil {
		t.Fatalf("ListInsights: %v", err)
	}
	if len(insights.GetInsights()) != 2 {
		t.Fatalf("insights = %#v", insights.GetInsights())
	}
	got := map[string]v1.Plausibility{}
	for _, in := range insights.GetInsights() {
		got[in.GetFindingKey()] = in.GetPlausibility()
	}
	if got["finding-1"] != v1.Plausibility_PLAUSIBILITY_DOUBTFUL || got["finding-2"] != v1.Plausibility_PLAUSIBILITY_UNSPECIFIED {
		t.Fatalf("plausibility = %v", got)
	}
	for _, in := range insights.GetInsights() {
		if in.GetFindingKey() == "finding-1" && in.GetStats().GetFields()["rho"].GetNumberValue() != 0.5 {
			t.Fatalf("stats = %#v", in.GetStats())
		}
	}

	workflows, err := client.ListWorkflows(ctx, &v1.ListWorkflowsRequest{ProjectId: projectID})
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(workflows.GetWorkflows()) != 2 {
		t.Fatalf("workflows = %#v", workflows.GetWorkflows())
	}
	steps, err := client.ListWorkflowSteps(ctx, &v1.ListWorkflowStepsRequest{WorkflowId: investigationWorkflowID})
	if err != nil {
		t.Fatalf("ListWorkflowSteps: %v", err)
	}
	if len(steps.GetSteps()) != 1 || steps.GetSteps()[0].GetOutput().GetFields()["records"].GetNumberValue() != 2 {
		t.Fatalf("steps = %#v", steps.GetSteps())
	}
}

func TestConnectCreateProjectIntegration(t *testing.T) {
	pool := testdb.Open(t)
	store := ingest.NewStore(pool)
	source, err := store.CreateSource(context.Background(), "Source for project", "rss", []byte(`{"url":"https://example.com/feed.xml"}`), intPtr(300))
	if err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer((&Server{Store: store}).Routes())
	t.Cleanup(httpServer.Close)
	client := connectTestClient(t, httpServer)

	created, err := client.CreateProject(context.Background(), &v1.CreateProjectRequest{
		Name: "Mobility study", Goal: "Understand travel patterns", SourceIds: []string{source.ID},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := created.GetProject()
	if project.GetId() == "" || project.GetName() != "Mobility study" || project.GetGoal() != "Understand travel patterns" {
		t.Fatalf("project = %#v", project)
	}
	if len(project.GetSourceIds()) != 1 || project.GetSourceIds()[0] != source.ID {
		t.Fatalf("source ids = %v, want [%s]", project.GetSourceIds(), source.ID)
	}
	if created.ActiveWorkflow != nil || created.LatestRun != nil {
		t.Fatalf("workflows/runs should not be in the create response: %#v", created)
	}

	detail, err := client.GetProject(context.Background(), &v1.GetProjectRequest{Id: project.GetId()})
	if err != nil {
		t.Fatalf("GetProject after create: %v", err)
	}
	if detail.GetProject().GetId() != project.GetId() || len(detail.GetActiveWorkflows()) != 0 {
		t.Fatalf("project detail = %#v", detail)
	}

	_, err = client.CreateProject(context.Background(), &v1.CreateProjectRequest{
		Name: "Invalid project", Goal: "Uses missing source", SourceIds: []string{"00000000-0000-0000-0000-000000000000"},
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("missing source code = %v, want InvalidArgument; err=%v", got, err)
	}
}

type createProjectAuthorStore struct{ loaded chan struct{} }

func (s *createProjectAuthorStore) LoadInputs(context.Context, uuid.UUID) (workflows.AuthorInputs, error) {
	s.loaded <- struct{}{}
	return workflows.AuthorInputs{}, nil
}

func (*createProjectAuthorStore) SaveVersion(context.Context, workflows.SaveVersion) (uuid.UUID, uuid.UUID, error) {
	return uuid.Nil, uuid.Nil, nil
}

func (*createProjectAuthorStore) SetProjectError(context.Context, uuid.UUID, string) error {
	return nil
}

type emptyAnalysisStore struct{ checked chan struct{} }

func (*emptyAnalysisStore) ProjectSources(context.Context, uuid.UUID) ([]analysis.SourceMeta, error) {
	return nil, nil
}

func (*emptyAnalysisStore) Records(context.Context, []uuid.UUID, time.Time, time.Time) (map[uuid.UUID][]analysis.Record, error) {
	return nil, nil
}

func (s *emptyAnalysisStore) MaxObservedAt(context.Context, uuid.UUID) (*time.Time, error) {
	s.checked <- struct{}{}
	return nil, nil
}

func TestConnectCreateProjectStartsStatisticalAuthor(t *testing.T) {
	pool := testdb.Open(t)
	store := ingest.NewStore(pool)
	source, err := store.CreateSource(context.Background(), "Source for author", "rss", []byte(`{"url":"https://example.com/feed.xml"}`), intPtr(300))
	if err != nil {
		t.Fatal(err)
	}
	authorStore := &createProjectAuthorStore{loaded: make(chan struct{}, 1)}
	analysisStore := &emptyAnalysisStore{checked: make(chan struct{}, 1)}
	author := &workflows.Author{
		Store:    authorStore,
		Analyser: &analysis.Analyser{Store: analysisStore},
	}
	httpServer := httptest.NewServer((&Server{Store: store, Author: author}).Routes())
	t.Cleanup(httpServer.Close)
	client := connectTestClient(t, httpServer)
	if _, err := client.CreateProject(context.Background(), &v1.CreateProjectRequest{
		Name: "Author starts", Goal: "Inspect source", SourceIds: []string{source.ID},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	select {
	case <-authorStore.loaded:
	case <-time.After(time.Second):
		t.Fatal("project creation did not start statistical workflow authoring")
	}
	select {
	case <-analysisStore.checked:
	case <-time.After(time.Second):
		t.Fatal("author did not check for source data")
	}
}

type forcedRunAnalyser struct{ called chan struct{} }

func (a *forcedRunAnalyser) Run(_ context.Context, _ uuid.UUID, _ string, dataUpTo time.Time) (contracts.AnalysisResult, error) {
	a.called <- struct{}{}
	return contracts.AnalysisResult{DataUpTo: dataUpTo, Funnel: map[string]int{}}, nil
}

func TestConnectRunWorkflowForcesRerunAtSameWatermark(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	var projectID, sourceID, workflowID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, goal) VALUES ('Force run', 'Recheck this data') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO data_sources (name, kind, config) VALUES ('Force source', 'csv', '{}'::jsonb) RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_sources (project_id, data_source_id) VALUES ($1, $2)`, projectID, sourceID); err != nil {
		t.Fatal(err)
	}
	watermark := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO data_records (data_source_id, external_id, observed_at, payload)
		VALUES ($1, 'force-row', $2, '{}'::jsonb)`, sourceID, watermark); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, analysis_script, insight_prompt, rationale)
		VALUES ($1, 'statistical', 1, 'result = {}', 'Explain', 'Force test') RETURNING id`, projectID).Scan(&workflowID); err != nil {
		t.Fatal(err)
	}
	var oldRunID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (workflow_id, data_up_to, status, result, started_at, finished_at)
		VALUES ($1, $2, 'succeeded', '{}'::jsonb, $3, $3) RETURNING id`,
		workflowID, watermark, watermark).Scan(&oldRunID); err != nil {
		t.Fatal(err)
	}

	analyser := &forcedRunAnalyser{called: make(chan struct{}, 1)}
	runner := &workflows.Runner{
		Store:    &workflows.PgRunStore{Pool: pool, Sources: &analysis.PgStore{Pool: pool}},
		Analyser: analyser,
	}
	runner.SetServiceContext(context.Background())
	httpServer := httptest.NewServer((&Server{Store: ingest.NewStore(pool), Runner: runner}).Routes())
	t.Cleanup(httpServer.Close)
	client := connectTestClient(t, httpServer)
	response, err := client.RunWorkflow(ctx, &v1.RunWorkflowRequest{
		ProjectId: projectID.String(), Kind: v1.WorkflowKind_WORKFLOW_KIND_STATISTICAL,
	})
	if err != nil || !response.GetAccepted() {
		t.Fatalf("RunWorkflow response = %#v, err=%v", response, err)
	}
	select {
	case <-analyser.called:
	case <-time.After(time.Second):
		t.Fatal("forced rerun did not execute the analyser")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		project, err := client.GetProject(ctx, &v1.GetProjectRequest{Id: projectID.String()})
		if err == nil && project.GetLatestRun().GetStatus() == v1.RunStatus_RUN_STATUS_SUCCEEDED && project.GetLatestRun().GetId() != oldRunID.String() {
			if !project.GetLatestRun().GetDataUpTo().AsTime().Equal(watermark) {
				t.Fatalf("rerun watermark = %v, want unchanged %v", project.GetLatestRun().GetDataUpTo().AsTime(), watermark)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("forced rerun did not persist a new succeeded run")
}
