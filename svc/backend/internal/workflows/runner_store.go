package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/insightrows"
)

// PgRunStore persists scheduler state and statistical run results.
type PgRunStore struct {
	Pool    *pgxpool.Pool
	Sources analysis.Store
}

var _ RunStore = (*PgRunStore)(nil)

func (s *PgRunStore) ProjectIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PgRunStore) ProjectsForSource(ctx context.Context, sourceID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT project_id FROM project_sources WHERE data_source_id = $1 ORDER BY project_id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PgRunStore) Watermark(ctx context.Context, projectID uuid.UUID) (time.Time, bool, error) {
	return analysis.Watermark(ctx, s.Sources, projectID)
}

func (s *PgRunStore) ActiveStatisticalWorkflow(ctx context.Context, projectID uuid.UUID) (*StatisticalWorkflow, error) {
	var workflow StatisticalWorkflow
	err := s.Pool.QueryRow(ctx, `
		SELECT id, project_id, analysis_script, insight_prompt
		FROM workflows
		WHERE project_id = $1 AND kind = 'statistical' AND is_active`, projectID).
		Scan(&workflow.ID, &workflow.ProjectID, &workflow.Script, &workflow.InsightPrompt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active statistical workflow: %w", err)
	}
	return &workflow, nil
}

func (s *PgRunStore) ProjectContext(ctx context.Context, projectID uuid.UUID) (RunProjectContext, error) {
	var out RunProjectContext
	if err := s.Pool.QueryRow(ctx, `SELECT goal FROM projects WHERE id = $1`, projectID).Scan(&out.Goal); err != nil {
		return out, fmt.Errorf("load project goal: %w", err)
	}
	metas, err := s.Sources.ProjectSources(ctx, projectID)
	if err != nil {
		return out, fmt.Errorf("load project sources: %w", err)
	}
	keys := analysis.SourceKeys(metas)
	out.Sources = make([]AuthorSource, 0, len(metas))
	for _, meta := range metas {
		profile := meta.Profile
		if profile.Summary == "" {
			profile.Summary = meta.Summary
		}
		out.Sources = append(out.Sources, AuthorSource{
			Key: keys[meta.ID], ID: meta.ID, Name: meta.Name, Kind: meta.Kind, Profile: profile,
		})
	}
	return out, nil
}

func (s *PgRunStore) State(ctx context.Context, workflowID uuid.UUID) (RunState, error) {
	var state RunState
	err := s.Pool.QueryRow(ctx, `
		SELECT
			(SELECT data_up_to FROM runs
			 WHERE workflow_id = $1 AND status = 'succeeded'
			 ORDER BY finished_at DESC NULLS LAST, started_at DESC LIMIT 1),
			COALESCE((SELECT status FROM runs
			          WHERE workflow_id = $1 ORDER BY started_at DESC, id DESC LIMIT 1), ''),
			(SELECT finished_at FROM runs
			 WHERE workflow_id = $1 ORDER BY started_at DESC, id DESC LIMIT 1),
			(SELECT count(*)::int FROM runs f
			 WHERE f.workflow_id = $1 AND f.status = 'failed'
			   AND f.started_at > COALESCE(
			     (SELECT max(s.finished_at) FROM runs s WHERE s.workflow_id = $1 AND s.status = 'succeeded'),
			     '-infinity'::timestamptz))`, workflowID).
		Scan(&state.SuccessfulDataUpTo, &state.LastStatus, &state.LastFinishedAt, &state.ConsecutiveFailures)
	if err != nil {
		return RunState{}, fmt.Errorf("load workflow run state: %w", err)
	}
	return state, nil
}

func (s *PgRunStore) StartRun(ctx context.Context, workflowID uuid.UUID, dataUpTo, startedAt time.Time) (uuid.UUID, error) {
	var runID uuid.UUID
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO runs (workflow_id, data_up_to, status, started_at)
		VALUES ($1, $2, 'running', $3) RETURNING id`, workflowID, dataUpTo, startedAt).Scan(&runID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert run: %w", err)
	}
	return runID, nil
}

func (s *PgRunStore) SucceedRun(ctx context.Context, projectID, runID uuid.UUID, result contracts.AnalysisResult, insights []InsightRow, finishedAt time.Time) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode analysis result: %w", err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin successful run transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if err := insightrows.Insert(ctx, tx, projectID, runID, insights); err != nil {
		return fmt.Errorf("insert insights: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE runs SET status = 'succeeded', result = $2, error = NULL, finished_at = $3
		WHERE id = $1 AND status = 'running'`, runID, resultJSON, finishedAt)
	if err != nil {
		return fmt.Errorf("finish successful run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("finish successful run: run %s is not running", runID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit successful run: %w", err)
	}
	return nil
}

func (s *PgRunStore) FailRun(ctx context.Context, runID uuid.UUID, message string, finishedAt time.Time) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE runs SET status = 'failed', error = $2, finished_at = $3
		WHERE id = $1 AND status = 'running'`, runID, message, finishedAt)
	if err != nil {
		return fmt.Errorf("mark run failed: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark run failed: run %s is not running", runID)
	}
	return nil
}

func (s *PgRunStore) ActiveInvestigationWorkflow(ctx context.Context, projectID uuid.UUID) (*InvestigationWorkflow, error) {
	var workflow InvestigationWorkflow
	err := s.Pool.QueryRow(ctx, `
		SELECT id, project_id FROM workflows
		WHERE project_id = $1 AND kind = 'investigation' AND is_active`, projectID).
		Scan(&workflow.ID, &workflow.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active investigation workflow: %w", err)
	}
	return &workflow, nil
}

func (s *PgRunStore) SucceedReplay(ctx context.Context, projectID, runID uuid.UUID, result contracts.ReplayResult, insights []InsightRow, finishedAt time.Time) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode replay result: %w", err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin successful run transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if err := insightrows.Insert(ctx, tx, projectID, runID, insights); err != nil {
		return fmt.Errorf("insert insights: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE runs SET status = 'succeeded', result = $2, error = NULL, finished_at = $3
		WHERE id = $1 AND status = 'running'`, runID, resultJSON, finishedAt)
	if err != nil {
		return fmt.Errorf("finish successful run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("finish successful run: run %s is not running", runID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit successful run: %w", err)
	}
	return nil
}
