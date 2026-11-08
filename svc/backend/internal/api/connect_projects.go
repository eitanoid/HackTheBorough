package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "hacktheburgh/backend/gen/hacktheburgh/v1"
)

func (s *ConnectService) CreateProject(_ context.Context, _ *v1.CreateProjectRequest) (*v1.CreateProjectResponse, error) {
	return nil, connect.NewError(connect.CodeFailedPrecondition, "project creation requires the workflow generation service, which is not configured")
}

func (s *ConnectService) RegenerateWorkflow(_ context.Context, _ *v1.RegenerateWorkflowRequest) (*v1.RegenerateWorkflowResponse, error) {
	return nil, connect.NewError(connect.CodeFailedPrecondition, "workflow regeneration service is not configured")
}

func (s *ConnectService) Replay(_ context.Context, _ *v1.ReplayRequest) (*v1.ReplayResponse, error) {
	return nil, connect.NewError(connect.CodeFailedPrecondition, "demo replay service is not configured")
}

func (s *ConnectService) ListProjects(ctx context.Context, _ *v1.ListProjectsRequest) (*v1.ListProjectsResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT p.id::text, p.name, p.goal, p.last_error, p.created_at,
		       COALESCE((SELECT r.status
		                 FROM runs r JOIN workflows w ON w.id = r.workflow_id
		                 WHERE w.project_id = p.id
		                 ORDER BY r.started_at DESC LIMIT 1), ''),
		       (SELECT COUNT(*) FROM latest_insights i WHERE i.project_id = p.id),
		       COALESCE((SELECT array_agg(ps.data_source_id::text ORDER BY ps.data_source_id)
		                 FROM project_sources ps WHERE ps.project_id = p.id), ARRAY[]::text[])
		FROM projects p ORDER BY p.created_at DESC`)
	if err != nil {
		return nil, internalError(err)
	}
	defer rows.Close()
	response := &v1.ListProjectsResponse{Projects: make([]*v1.ProjectSummary, 0)}
	for rows.Next() {
		var projectID, name, goal, status string
		var lastError *string
		var createdAt time.Time
		var findingCount int64
		var sourceIDs []string
		if err := rows.Scan(&projectID, &name, &goal, &lastError, &createdAt, &status, &findingCount, &sourceIDs); err != nil {
			return nil, internalError(err)
		}
		response.Projects = append(response.Projects, &v1.ProjectSummary{
			Project: &v1.Project{Id: projectID, Name: name, Goal: goal, LastError: stringValue(lastError),
				SourceIds: sourceIDs, CreatedAt: timestamppb.New(createdAt)},
			LatestRunStatus: runStatus(status), FindingCount: int32(findingCount),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, internalError(err)
	}
	return response, nil
}

func (s *ConnectService) GetProject(ctx context.Context, req *v1.GetProjectRequest) (*v1.GetProjectResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "id is required")
	}
	var name, goal string
	var lastError *string
	var createdAt time.Time
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, name, goal, last_error, created_at FROM projects WHERE id = $1::uuid`, id).
		Scan(&id, &name, &goal, &lastError, &createdAt)
	if err != nil {
		return nil, lookupError(err, "project")
	}
	project, err := s.projectMessage(ctx, id, name, goal, lastError, createdAt)
	if err != nil {
		return nil, internalError(err)
	}
	response := &v1.GetProjectResponse{Project: project, ActiveWorkflows: make([]*v1.Workflow, 0)}
	workflows, err := s.loadWorkflows(ctx, "project_id", id, true)
	if err != nil {
		return nil, internalError(err)
	}
	response.ActiveWorkflows = workflows
	run, result, err := s.latestRun(ctx, id)
	if err != nil {
		return nil, internalError(err)
	}
	response.LatestRun = run
	if result != nil {
		response.Funnel = analysisFunnel(result)
	}
	return response, nil
}

func (s *ConnectService) ListInsights(ctx context.Context, req *v1.ListInsightsRequest) (*v1.ListInsightsResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	projectID := strings.TrimSpace(req.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "project_id is required")
	}
	if err := s.ensureExists(ctx, "projects", projectID, "project"); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, run_id::text, project_id::text, origin, finding_key, kind,
		       headline, explanation, caveats, stats, evidence, chart_spec, narrated_by, created_at, plausibility
		FROM latest_insights WHERE project_id = $1::uuid ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, lookupError(err, "project")
	}
	defer rows.Close()
	response := &v1.ListInsightsResponse{Insights: make([]*v1.Insight, 0)}
	for rows.Next() {
		var id, runID, storedProjectID, origin, findingKey, kind, headline, explanation, narratedBy string
		var caveatsRaw, statsRaw, evidenceRaw, chartRaw []byte
		var createdAt time.Time
		var plausibility *string
		if err := rows.Scan(&id, &runID, &storedProjectID, &origin, &findingKey, &kind,
			&headline, &explanation, &caveatsRaw, &statsRaw, &evidenceRaw, &chartRaw, &narratedBy, &createdAt, &plausibility); err != nil {
			return nil, internalError(err)
		}
		var caveats []string
		if err := json.Unmarshal(caveatsRaw, &caveats); err != nil {
			return nil, internalError(err)
		}
		stats, err := jsonStruct(statsRaw)
		if err != nil {
			return nil, internalError(err)
		}
		chart, err := jsonStruct(chartRaw)
		if err != nil {
			return nil, internalError(err)
		}
		var evidence *structpb.Value
		if len(evidenceRaw) > 0 && string(evidenceRaw) != "null" {
			var value any
			if err := json.Unmarshal(evidenceRaw, &value); err != nil {
				return nil, internalError(err)
			}
			evidence, err = structpb.NewValue(value)
			if err != nil {
				return nil, internalError(err)
			}
		}
		response.Insights = append(response.Insights, &v1.Insight{
			Id: id, RunId: runID, ProjectId: storedProjectID,
			Origin: insightOrigin(origin), FindingKey: findingKey, Kind: insightKind(kind),
			Headline: headline, Explanation: explanation, Caveats: caveats,
			Stats: stats, Evidence: evidence, ChartSpec: chart,
			NarratedBy: narratedByValue(narratedBy), CreatedAt: timestamppb.New(createdAt),
			Plausibility: plausibilityValue(plausibility),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, internalError(err)
	}
	return response, nil
}

func (s *ConnectService) ListWorkflows(ctx context.Context, req *v1.ListWorkflowsRequest) (*v1.ListWorkflowsResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	projectID := strings.TrimSpace(req.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "project_id is required")
	}
	if err := s.ensureExists(ctx, "projects", projectID, "project"); err != nil {
		return nil, err
	}
	workflows, err := s.loadWorkflows(ctx, "project_id", projectID, false)
	if err != nil {
		return nil, lookupError(err, "project")
	}
	return &v1.ListWorkflowsResponse{Workflows: workflows}, nil
}

func (s *ConnectService) ListWorkflowSteps(ctx context.Context, req *v1.ListWorkflowStepsRequest) (*v1.ListWorkflowStepsResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	workflowID := strings.TrimSpace(req.GetWorkflowId())
	if workflowID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "workflow_id is required")
	}
	if err := s.ensureExists(ctx, "workflows", workflowID, "workflow"); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT workflow_id::text, seq, tool, input, output
		FROM workflow_steps WHERE workflow_id = $1::uuid ORDER BY seq`, workflowID)
	if err != nil {
		return nil, lookupError(err, "workflow")
	}
	defer rows.Close()
	response := &v1.ListWorkflowStepsResponse{Steps: make([]*v1.WorkflowStep, 0)}
	for rows.Next() {
		var step v1.WorkflowStep
		var inputRaw, outputRaw []byte
		if err := rows.Scan(&step.WorkflowId, &step.Seq, &step.Tool, &inputRaw, &outputRaw); err != nil {
			return nil, internalError(err)
		}
		step.Input, err = jsonStruct(inputRaw)
		if err != nil {
			return nil, internalError(err)
		}
		step.Output, err = jsonStruct(outputRaw)
		if err != nil {
			return nil, internalError(err)
		}
		response.Steps = append(response.Steps, &step)
	}
	if err := rows.Err(); err != nil {
		return nil, internalError(err)
	}
	return response, nil
}

func (s *ConnectService) projectMessage(ctx context.Context, id, name, goal string, lastError *string, createdAt time.Time) (*v1.Project, error) {
	rows, err := s.Store.Pool.Query(ctx, `SELECT data_source_id::text FROM project_sources WHERE project_id = $1::uuid ORDER BY data_source_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sourceIDs := make([]string, 0)
	for rows.Next() {
		var sourceID string
		if err := rows.Scan(&sourceID); err != nil {
			return nil, err
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &v1.Project{
		Id: id, Name: name, Goal: goal, SourceIds: sourceIDs,
		LastError: stringValue(lastError), CreatedAt: timestamppb.New(createdAt),
	}, nil
}

func (s *ConnectService) ensureExists(ctx context.Context, table, id, resource string) error {
	if table != "projects" && table != "workflows" {
		return fmt.Errorf("invalid existence check table %q", table)
	}
	var exists bool
	err := s.Store.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id = $1::uuid)", id).Scan(&exists)
	if err != nil {
		return lookupError(err, resource)
	}
	if !exists {
		return connect.NewError(connect.CodeNotFound, resource+" not found")
	}
	return nil
}

func (s *ConnectService) loadWorkflows(ctx context.Context, filter string, id string, activeOnly bool) ([]*v1.Workflow, error) {
	var query string
	switch filter {
	case "project_id":
		query = `SELECT id::text, project_id::text, kind, version, is_active, insight_prompt,
		                rationale, created_at, analysis_script, report
		         FROM workflows WHERE project_id = $1::uuid`
	default:
		return nil, errors.New("unsupported workflow filter")
	}
	if activeOnly {
		query += " AND is_active"
	}
	query += " ORDER BY kind, version DESC"
	rows, err := s.Store.Pool.Query(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workflows := make([]*v1.Workflow, 0)
	for rows.Next() {
		var workflow v1.Workflow
		var kind string
		var createdAt time.Time
		var script *string
		var reportRaw []byte
		if err := rows.Scan(&workflow.Id, &workflow.ProjectId, &kind, &workflow.Version, &workflow.IsActive,
			&workflow.InsightPrompt, &workflow.Rationale, &createdAt, &script, &reportRaw); err != nil {
			return nil, err
		}
		workflow.Kind = workflowKind(kind)
		workflow.CreatedAt = timestamppb.New(createdAt)
		if script != nil {
			workflow.AnalysisScript = *script
		}
		workflow.Report, err = jsonStruct(reportRaw)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, &workflow)
	}
	return workflows, rows.Err()
}

func (s *ConnectService) latestRun(ctx context.Context, projectID string) (*v1.Run, map[string]any, error) {
	var run v1.Run
	var status string
	var resultRaw []byte
	var runError *string
	var dataUpTo, startedAt time.Time
	var finishedAt *time.Time
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT r.id::text, r.workflow_id::text, r.data_up_to, r.status, r.result,
	       r.error, r.started_at, r.finished_at
		FROM runs r JOIN workflows w ON w.id = r.workflow_id
		WHERE w.project_id = $1::uuid ORDER BY r.started_at DESC LIMIT 1`, projectID).
		Scan(&run.Id, &run.WorkflowId, &dataUpTo, &status, &resultRaw, &runError, &startedAt, &finishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	run.DataUpTo = timestamppb.New(dataUpTo)
	run.Status = runStatus(status)
	run.StartedAt = timestamppb.New(startedAt)
	run.Error = stringValue(runError)
	if finishedAt != nil {
		run.FinishedAt = timestamppb.New(*finishedAt)
	}
	result, err := jsonMap(resultRaw)
	if err != nil {
		return nil, nil, err
	}
	if result != nil {
		run.Result, err = structpb.NewStruct(result)
		if err != nil {
			return nil, nil, err
		}
	}
	return &run, result, nil
}

func analysisFunnel(result map[string]any) *v1.AnalysisFunnel {
	funnel, _ := result["funnel"].(map[string]any)
	return &v1.AnalysisFunnel{
		TestsRun:        int32(number(funnel["tests_run"])),
		RawPLt_05:       int32(number(funnel["raw_p_lt_05"])),
		PassedFdr:       int32(number(funnel["passed_fdr"])),
		PassedStability: int32(number(funnel["passed_stability"])),
	}
}

func jsonStruct(raw []byte) (*structpb.Struct, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	return structpb.NewStruct(value)
}

func jsonMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func number(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	default:
		return 0
	}
}

func workflowKind(kind string) v1.WorkflowKind {
	switch kind {
	case "statistical":
		return v1.WorkflowKind_WORKFLOW_KIND_STATISTICAL
	case "investigation":
		return v1.WorkflowKind_WORKFLOW_KIND_INVESTIGATION
	default:
		return v1.WorkflowKind_WORKFLOW_KIND_UNSPECIFIED
	}
}

func runStatus(status string) v1.RunStatus {
	switch status {
	case "running":
		return v1.RunStatus_RUN_STATUS_RUNNING
	case "succeeded":
		return v1.RunStatus_RUN_STATUS_SUCCEEDED
	case "failed":
		return v1.RunStatus_RUN_STATUS_FAILED
	default:
		return v1.RunStatus_RUN_STATUS_UNSPECIFIED
	}
}

func insightOrigin(origin string) v1.InsightOrigin {
	switch origin {
	case "statistical":
		return v1.InsightOrigin_INSIGHT_ORIGIN_STATISTICAL
	case "investigation":
		return v1.InsightOrigin_INSIGHT_ORIGIN_INVESTIGATION
	default:
		return v1.InsightOrigin_INSIGHT_ORIGIN_UNSPECIFIED
	}
}

func insightKind(kind string) v1.InsightKind {
	switch kind {
	case "correlation":
		return v1.InsightKind_INSIGHT_KIND_CORRELATION
	case "anomaly":
		return v1.InsightKind_INSIGHT_KIND_ANOMALY
	case "observation":
		return v1.InsightKind_INSIGHT_KIND_OBSERVATION
	case "custom":
		return v1.InsightKind_INSIGHT_KIND_CUSTOM
	default:
		return v1.InsightKind_INSIGHT_KIND_UNSPECIFIED
	}
}

func narratedByValue(value string) v1.NarratedBy {
	switch value {
	case "llm":
		return v1.NarratedBy_NARRATED_BY_LLM
	case "template":
		return v1.NarratedBy_NARRATED_BY_TEMPLATE
	default:
		return v1.NarratedBy_NARRATED_BY_UNSPECIFIED
	}
}

func plausibilityValue(value *string) v1.Plausibility {
	if value == nil {
		return v1.Plausibility_PLAUSIBILITY_UNSPECIFIED
	}
	switch *value {
	case "plausible":
		return v1.Plausibility_PLAUSIBILITY_PLAUSIBLE
	case "doubtful":
		return v1.Plausibility_PLAUSIBILITY_DOUBTFUL
	default:
		return v1.Plausibility_PLAUSIBILITY_UNSPECIFIED
	}
}

func lookupError(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, resource+" not found")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return connect.NewError(connect.CodeInvalidArgument, resource+" id must be a UUID")
	}
	return internalError(fmt.Errorf("lookup %s: %w", resource, err))
}
