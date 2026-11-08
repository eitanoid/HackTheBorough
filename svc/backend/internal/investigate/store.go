package investigate

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
	"hacktheburgh/backend/internal/narration"
)

// ErrorPrefix starts every projects.last_error the investigator writes, so a
// successful save clears only its own errors, not the author's.
const ErrorPrefix = "investigation: "

type Source struct {
	Key     string // key in INPUT['sources']
	Name    string
	Kind    string
	Profile contracts.SourceProfile
	Records int
	FirstAt time.Time // zero when Records is 0
	LastAt  time.Time
}

// Lead is a statistical finding the investigator may test further. Effect
// sizes are left out on purpose: the model computes its own numbers.
type Lead struct {
	Key       string
	Kind      string
	Series    []string // "Label (unit)"
	Direction string
	Q         float64
	HasQ      bool
}

type Inputs struct {
	Goal          string
	Sources       []Source
	SourceIDs     []uuid.UUID
	InsightPrompt string // the active statistical workflow's, for context
	Leads         []Lead
}

// SaveVersion is everything one generation stores, in one transaction.
type SaveVersion struct {
	ProjectID uuid.UUID
	Report    contracts.InvestigationReport // cleaned
	Brief     string                        // workflows.insight_prompt
	SourceIDs []uuid.UUID
	Steps     []Step
	DataUpTo  time.Time
	StartedAt time.Time
	Replay    contracts.ReplayResult // the first run's result
	// Cards builds the first run's cards once the version number is known.
	Cards    func(version int) []insightrows.InsightRow
	ReviewID *uuid.UUID
}

type Saved struct {
	WorkflowID, RunID uuid.UUID
	Version           int
}

// Workflow is an investigation version as the replayer needs it.
type Workflow struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Version   int
	CreatedAt time.Time
	Report    contracts.InvestigationReport
	Steps     []Step
}

type Store interface {
	LoadInputs(ctx context.Context, projectID uuid.UUID) (Inputs, error)
	// HasInvestigation reports whether the project has ever had an
	// investigation workflow, active or not.
	HasInvestigation(ctx context.Context, projectID uuid.UUID) (bool, error)
	// SaveVersion writes the workflow, its steps, the first run and its cards
	// in one transaction and makes the new version the only active one.
	SaveVersion(ctx context.Context, v SaveVersion) (Saved, error)
	SetProjectError(ctx context.Context, projectID uuid.UUID, msg string) error
}

type ReplayStore interface {
	LoadWorkflow(ctx context.Context, workflowID uuid.UUID) (Workflow, error)
	// PreviousReplay is the latest succeeded run's result; nil when there is none.
	PreviousReplay(ctx context.Context, workflowID uuid.UUID) (*contracts.ReplayResult, error)
}

type PgStore struct {
	Pool    *pgxpool.Pool
	Sources analysis.Store
}

var (
	_ Store       = (*PgStore)(nil)
	_ ReplayStore = (*PgStore)(nil)
)

func (s *PgStore) LoadInputs(ctx context.Context, projectID uuid.UUID) (Inputs, error) {
	var in Inputs
	if err := s.Pool.QueryRow(ctx, `SELECT goal FROM projects WHERE id = $1`, projectID).Scan(&in.Goal); err != nil {
		return in, fmt.Errorf("load project: %w", err)
	}
	metas, err := s.Sources.ProjectSources(ctx, projectID)
	if err != nil {
		return in, fmt.Errorf("load sources: %w", err)
	}
	keys := analysis.SourceKeys(metas)
	for _, m := range metas {
		src := Source{Key: keys[m.ID], Name: m.Name, Kind: m.Kind, Profile: m.Profile}
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

	err = s.Pool.QueryRow(ctx, `
		SELECT insight_prompt FROM workflows
		WHERE project_id = $1 AND kind = 'statistical' AND is_active`, projectID).Scan(&in.InsightPrompt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return in, fmt.Errorf("load statistical workflow: %w", err)
	}

	var raw []byte
	err = s.Pool.QueryRow(ctx, `
		SELECT r.result FROM runs r JOIN workflows w ON w.id = r.workflow_id
		WHERE w.project_id = $1 AND w.kind = 'statistical' AND r.status = 'succeeded' AND r.result IS NOT NULL
		ORDER BY r.finished_at DESC NULLS LAST, r.started_at DESC LIMIT 1`, projectID).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return in, fmt.Errorf("load statistical run: %w", err)
	default:
		var res contracts.AnalysisResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return in, fmt.Errorf("decode statistical run: %w", err)
		}
		in.Leads = LeadsFrom(res)
	}
	return in, nil
}

// LeadsFrom lists a statistical run's findings as leads.
func LeadsFrom(res contracts.AnalysisResult) []Lead {
	labels := make(map[string]string, len(res.Health.Series))
	for _, s := range res.Health.Series {
		l := s.Label
		if s.Unit != nil && *s.Unit != "" {
			l += " (" + *s.Unit + ")"
		}
		labels[s.ID] = l
	}
	leads := make([]Lead, 0, len(res.Findings))
	for _, f := range res.Findings {
		l := Lead{Key: f.FindingKey, Kind: f.Kind, Direction: narration.Direction(f)}
		for _, id := range f.Series {
			name := labels[id]
			if name == "" {
				name = id
			}
			l.Series = append(l.Series, name)
		}
		l.Q, l.HasQ = narration.Number(f.Stats["q"])
		leads = append(leads, l)
	}
	return leads
}

func (s *PgStore) HasInvestigation(ctx context.Context, projectID uuid.UUID) (bool, error) {
	var has bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM workflows WHERE project_id = $1 AND kind = 'investigation')`, projectID).Scan(&has)
	return has, err
}

// SaveVersion mirrors PgAuthorStore.SaveVersion: the one_active_workflow
// index rejects a second active row, so the old version is deactivated first,
// and the project row is locked so two saves can't pick the same version.
func (s *PgStore) SaveVersion(ctx context.Context, v SaveVersion) (Saved, error) {
	var out Saved
	report, err := json.Marshal(v.Report)
	if err != nil {
		return out, err
	}
	result, err := json.Marshal(v.Replay)
	if err != nil {
		return out, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE id = $1 FOR UPDATE`, v.ProjectID).Scan(&locked); err != nil {
		return out, fmt.Errorf("lock project: %w", err)
	}
	var version int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM workflows WHERE project_id = $1 AND kind = 'investigation'`,
		v.ProjectID).Scan(&version); err != nil {
		return out, err
	}
	out.Version = version + 1
	if _, err := tx.Exec(ctx,
		`UPDATE workflows SET is_active = false WHERE project_id = $1 AND kind = 'investigation' AND is_active`,
		v.ProjectID); err != nil {
		return out, err
	}
	ids := v.SourceIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workflows (project_id, kind, version, report, insight_prompt, rationale, source_ids)
		VALUES ($1, 'investigation', $2, $3, $4, $5, $6) RETURNING id`,
		v.ProjectID, out.Version, report, v.Brief, v.Report.Rationale, ids).Scan(&out.WorkflowID); err != nil {
		return out, fmt.Errorf("insert workflow: %w", err)
	}
	for _, st := range v.Steps {
		if _, err := tx.Exec(ctx, `
			INSERT INTO workflow_steps (workflow_id, seq, tool, input, output) VALUES ($1, $2, $3, $4, $5)`,
			out.WorkflowID, st.Seq, st.Tool, []byte(st.Input), []byte(st.Output)); err != nil {
			return out, fmt.Errorf("insert step %d: %w", st.Seq, err)
		}
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO runs (workflow_id, data_up_to, status, result, started_at, finished_at)
		VALUES ($1, $2, 'succeeded', $3, $4, now()) RETURNING id`,
		out.WorkflowID, v.DataUpTo, result, v.StartedAt).Scan(&out.RunID); err != nil {
		return out, fmt.Errorf("insert run: %w", err)
	}
	if v.Cards != nil {
		if err := insightrows.Insert(ctx, tx, v.ProjectID, out.RunID, v.Cards(out.Version)); err != nil {
			return out, err
		}
	}
	if v.ReviewID != nil {
		if _, err := tx.Exec(ctx, `UPDATE reviews SET next_workflow_id = $1 WHERE id = $2`, out.WorkflowID, *v.ReviewID); err != nil {
			return out, err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE projects SET last_error = NULL WHERE id = $1 AND last_error LIKE $2`, v.ProjectID, ErrorPrefix+"%"); err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Saved{}, err
	}
	return out, nil
}

func (s *PgStore) SetProjectError(ctx context.Context, projectID uuid.UUID, msg string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE projects SET last_error = $2 WHERE id = $1`, projectID, msg)
	return err
}

func (s *PgStore) LoadWorkflow(ctx context.Context, workflowID uuid.UUID) (Workflow, error) {
	w := Workflow{ID: workflowID}
	var report []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT project_id, version, created_at, report FROM workflows WHERE id = $1 AND kind = 'investigation'`, workflowID).
		Scan(&w.ProjectID, &w.Version, &w.CreatedAt, &report)
	if err != nil {
		return w, fmt.Errorf("load investigation workflow: %w", err)
	}
	if err := json.Unmarshal(report, &w.Report); err != nil {
		return w, fmt.Errorf("decode report: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT seq, tool, input, output FROM workflow_steps WHERE workflow_id = $1 ORDER BY seq`, workflowID)
	if err != nil {
		return w, fmt.Errorf("load steps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.Seq, &st.Tool, &st.Input, &st.Output); err != nil {
			return w, err
		}
		w.Steps = append(w.Steps, st)
	}
	return w, rows.Err()
}

func (s *PgStore) PreviousReplay(ctx context.Context, workflowID uuid.UUID) (*contracts.ReplayResult, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT result FROM runs WHERE workflow_id = $1 AND status = 'succeeded' AND result IS NOT NULL
		ORDER BY finished_at DESC NULLS LAST, started_at DESC LIMIT 1`, workflowID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load previous replay: %w", err)
	}
	var r contracts.ReplayResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode previous replay: %w", err)
	}
	return &r, nil
}
