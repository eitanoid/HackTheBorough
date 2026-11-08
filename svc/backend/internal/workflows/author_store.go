package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
)

type AuthorSource struct {
	Key     string // key in INPUT['sources']
	ID      uuid.UUID
	Name    string
	Kind    string
	Profile contracts.SourceProfile
	Records int
	FirstAt time.Time // zero when Records is 0
	LastAt  time.Time
}

type AuthorInputs struct {
	Goal        string
	Sources     []AuthorSource
	SourceIDs   []uuid.UUID
	PrevScript  *string // active statistical version, if any
	PrevVersion int     // 0 if none
}

type SaveVersion struct {
	ProjectID uuid.UUID
	Draft     contracts.WorkflowDraft
	SourceIDs []uuid.UUID
	DataUpTo  time.Time
	Result    contracts.AnalysisResult
	StartedAt time.Time
	ReviewID  *uuid.UUID
}

type AuthorStore interface {
	LoadInputs(ctx context.Context, projectID uuid.UUID) (AuthorInputs, error)
	// SaveVersion writes the new workflow and its run in one transaction and
	// makes the new version the only active one.
	SaveVersion(ctx context.Context, v SaveVersion) (workflowID, runID uuid.UUID, err error)
	SetProjectError(ctx context.Context, projectID uuid.UUID, msg string) error
}

type PgAuthorStore struct {
	Pool *pgxpool.Pool
	// Sources supplies the project's sources; it is the same store the
	// analyser reads, so the author and the script agree on source keys.
	Sources analysis.Store
}

var _ AuthorStore = (*PgAuthorStore)(nil)

func (s *PgAuthorStore) LoadInputs(ctx context.Context, projectID uuid.UUID) (AuthorInputs, error) {
	var in AuthorInputs
	if err := s.Pool.QueryRow(ctx, `SELECT goal FROM projects WHERE id = $1`, projectID).Scan(&in.Goal); err != nil {
		return in, fmt.Errorf("load project: %w", err)
	}
	metas, err := s.Sources.ProjectSources(ctx, projectID)
	if err != nil {
		return in, fmt.Errorf("load sources: %w", err)
	}
	keys := analysis.SourceKeys(metas)
	for _, m := range metas {
		src := AuthorSource{Key: keys[m.ID], ID: m.ID, Name: m.Name, Kind: m.Kind, Profile: m.Profile}
		if m.Profile.Summary == "" {
			src.Profile.Summary = m.Summary
		}
		var first, last *time.Time
		if err := s.Pool.QueryRow(ctx,
			`SELECT count(*), min(observed_at), max(observed_at) FROM data_records WHERE data_source_id = $1`, m.ID,
		).Scan(&src.Records, &first, &last); err != nil {
			return in, fmt.Errorf("count records: %w", err)
		}
		if first != nil && last != nil {
			src.FirstAt, src.LastAt = *first, *last
		}
		in.Sources = append(in.Sources, src)
		in.SourceIDs = append(in.SourceIDs, m.ID)
	}

	var script string
	err = s.Pool.QueryRow(ctx, `
		SELECT analysis_script, version FROM workflows
		WHERE project_id = $1 AND kind = 'statistical' AND is_active`, projectID).Scan(&script, &in.PrevVersion)
	switch {
	case err == nil:
		in.PrevScript = &script
	case err != pgx.ErrNoRows:
		return in, fmt.Errorf("load active workflow: %w", err)
	}
	return in, nil
}

// SaveVersion's order matters: the one_active_workflow partial unique index
// rejects a second active row, so the old version is deactivated before the
// new one is inserted. The project row is locked so two saves cannot pick the
// same version number.
func (s *PgAuthorStore) SaveVersion(ctx context.Context, v SaveVersion) (uuid.UUID, uuid.UUID, error) {
	var wfID, runID uuid.UUID
	result, err := json.Marshal(v.Result)
	if err != nil {
		return wfID, runID, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return wfID, runID, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE id = $1 FOR UPDATE`, v.ProjectID).Scan(&locked); err != nil {
		return wfID, runID, fmt.Errorf("lock project: %w", err)
	}
	var version int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM workflows WHERE project_id = $1 AND kind = 'statistical'`,
		v.ProjectID).Scan(&version); err != nil {
		return wfID, runID, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE workflows SET is_active = false WHERE project_id = $1 AND kind = 'statistical' AND is_active`,
		v.ProjectID); err != nil {
		return wfID, runID, err
	}
	ids := v.SourceIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, analysis_script, insight_prompt, rationale, source_ids)
		VALUES ($1, 'statistical', $2, $3, $4, $5, $6) RETURNING id`,
		v.ProjectID, version+1, v.Draft.Script, v.Draft.InsightPrompt, v.Draft.Rationale, ids).Scan(&wfID); err != nil {
		return wfID, runID, fmt.Errorf("insert workflow: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO runs (workflow_id, data_up_to, status, result, started_at, finished_at)
		VALUES ($1, $2, 'succeeded', $3, $4, now()) RETURNING id`,
		wfID, v.DataUpTo, result, v.StartedAt).Scan(&runID); err != nil {
		return wfID, runID, fmt.Errorf("insert run: %w", err)
	}
	if v.ReviewID != nil {
		if _, err := tx.Exec(ctx, `UPDATE reviews SET next_workflow_id = $1 WHERE id = $2`, wfID, *v.ReviewID); err != nil {
			return wfID, runID, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET last_error = NULL WHERE id = $1`, v.ProjectID); err != nil {
		return wfID, runID, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.UUID{}, uuid.UUID{}, err
	}
	return wfID, runID, nil
}

func (s *PgAuthorStore) SetProjectError(ctx context.Context, projectID uuid.UUID, msg string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE projects SET last_error = $2 WHERE id = $1`, projectID, msg)
	return err
}
