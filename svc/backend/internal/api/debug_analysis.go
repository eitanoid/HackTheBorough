package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/demo"
	"hacktheburgh/backend/internal/workflows"
)

// The endpoints below exist so the statistical author can be exercised end to
// end in a dev deployment. They are registered only when ANALYSIS_DEBUG=1 and
// must stay off anywhere else: they write to the database and spend LLM budget
// for any caller.
func (s *Server) registerAnalysisDebug(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/debug/seed", s.debugSeed)
	mux.HandleFunc("POST /api/debug/projects/{id}/generate", s.debugGenerate)
	mux.HandleFunc("GET /api/debug/projects/{id}/state", s.debugState)
	mux.HandleFunc("POST /api/debug/projects/{id}/narrate", s.debugNarrate)
}

func (s *Server) debugSeed(w http.ResponseWriter, r *http.Request) {
	seeded, err := demo.SeedStrikeDemo(r.Context(), s.Pool, time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, seeded)
}

// Add ?reason=... to run it as a regeneration (it needs an active version), and
// ?model=... to author with a different model than configured.
func (s *Server) debugGenerate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad project id"})
		return
	}
	if s.Author == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the author is disabled (no LLM configured)"})
		return
	}
	var trigger *workflows.Trigger
	if reason := r.URL.Query().Get("reason"); reason != "" {
		trigger = &workflows.Trigger{Reason: reason}
	}

	author, model := s.Author, r.URL.Query().Get("model")
	if model != "" {
		author = author.WithModel(model)
	}
	start := time.Now()
	genErr := author.GenerateNow(r.Context(), id, trigger)
	resp := map[string]any{"duration_ms": time.Since(start).Milliseconds(), "model": model}
	status := http.StatusOK
	switch {
	case errors.Is(genErr, workflows.ErrBusy):
		status = http.StatusConflict
	case errors.Is(genErr, workflows.ErrNoData):
		status = http.StatusConflict
	case genErr != nil:
		status = http.StatusUnprocessableEntity
	}
	if genErr != nil {
		resp["error"] = genErr.Error()
	}
	if state, err := s.loadDebugState(r, id); err == nil {
		resp["state"] = state
	}
	writeJSON(w, status, resp)
}

func (s *Server) debugState(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad project id"})
		return
	}
	state, err := s.loadDebugState(r, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such project"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) loadDebugState(r *http.Request, id uuid.UUID) (map[string]any, error) {
	ctx := r.Context()
	var name, goal string
	var lastErr *string
	if err := s.Pool.QueryRow(ctx, `SELECT name, goal, last_error FROM projects WHERE id = $1`, id).Scan(&name, &goal, &lastErr); err != nil {
		return nil, err
	}
	state := map[string]any{"project": map[string]any{"id": id, "name": name, "goal": goal, "last_error": lastErr}, "workflow": nil, "run": nil, "insights": nil}

	var wfID uuid.UUID
	var version int
	var script, insight, rationale string
	var sourceIDs []uuid.UUID
	var created time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT id, version, analysis_script, insight_prompt, rationale, source_ids, created_at
		FROM workflows WHERE project_id = $1 AND kind = 'statistical' AND is_active`, id).
		Scan(&wfID, &version, &script, &insight, &rationale, &sourceIDs, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	state["workflow"] = map[string]any{"id": wfID, "version": version, "script": script, "insight_prompt": insight,
		"rationale": rationale, "source_ids": sourceIDs, "created_at": created}

	var runID uuid.UUID
	var status string
	var dataUpTo time.Time
	var result []byte
	var runErr *string
	err = s.Pool.QueryRow(ctx, `
		SELECT id, status, data_up_to, result, error FROM runs WHERE workflow_id = $1 ORDER BY started_at DESC LIMIT 1`, wfID).
		Scan(&runID, &status, &dataUpTo, &result, &runErr)
	if err == nil {
		state["run"] = map[string]any{"id": runID, "status": status, "data_up_to": dataUpTo, "error": runErr, "result": json.RawMessage(result)}
		insights, err := s.loadDebugInsights(r, runID)
		if err != nil {
			return nil, err
		}
		state["insights"] = insights
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return state, nil
}

// loadDebugInsights returns the cards stored with a run, as the narrator left
// them: text, verdict, and whether it came from the LLM or the template.
func (s *Server) loadDebugInsights(r *http.Request, runID uuid.UUID) ([]map[string]any, error) {
	rows, err := s.Pool.Query(r.Context(), `
		SELECT finding_key, kind, headline, explanation, caveats, stats, narrated_by, plausibility, chart_spec IS NOT NULL
		FROM insights WHERE run_id = $1 AND origin = 'statistical' ORDER BY finding_key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, kind, headline, explanation, narratedBy string
		var caveats, stats []byte
		var plausibility *string
		var hasChart bool
		if err := rows.Scan(&key, &kind, &headline, &explanation, &caveats, &stats, &narratedBy, &plausibility, &hasChart); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"finding_key": key, "kind": kind, "headline": headline, "explanation": explanation,
			"caveats": json.RawMessage(caveats), "stats": json.RawMessage(stats),
			"narrated_by": narratedBy, "plausibility": plausibility, "has_chart": hasChart,
		})
	}
	return out, rows.Err()
}

// debugNarrate runs the narrator over the latest run's findings of the active
// statistical workflow and returns the prompts and the cards. Nothing is stored,
// and earlier texts are not reused, so it always makes one LLM call. Add
// ?model=... to narrate with a different model than configured.
func (s *Server) debugNarrate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad project id"})
		return
	}
	if s.Author == nil || s.Author.Insights == nil || s.Author.Insights.Narrator == nil || s.Author.Insights.Narrator.LLM == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the narrator is disabled (no LLM configured)"})
		return
	}
	ctx := r.Context()
	var insightPrompt string
	var result []byte
	err = s.Pool.QueryRow(ctx, `
		SELECT w.insight_prompt, r.result
		FROM workflows w JOIN runs r ON r.workflow_id = w.id
		WHERE w.project_id = $1 AND w.kind = 'statistical' AND w.is_active AND r.status = 'succeeded'
		ORDER BY r.finished_at DESC LIMIT 1`, id).Scan(&insightPrompt, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the project has no succeeded statistical run; generate first"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var res contracts.AnalysisResult
	if err := json.Unmarshal(result, &res); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stored result is unreadable: " + err.Error()})
		return
	}
	in, err := s.Author.Store.LoadInputs(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	model := r.URL.Query().Get("model")
	start := time.Now()
	preview := s.Author.Insights.Preview(ctx, workflows.NarrationContext(in.Goal, insightPrompt, in.Sources, res), res.Findings, model)
	writeJSON(w, http.StatusOK, map[string]any{
		"duration_ms": time.Since(start).Milliseconds(), "model": model, "findings": len(res.Findings), "preview": preview,
	})
}
