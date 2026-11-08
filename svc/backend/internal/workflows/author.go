package workflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/llm"
)

var (
	ErrBusy   = errors.New("a statistical workflow is already being generated for this project")
	ErrNoData = errors.New("project has no records yet")
)

type Trigger struct {
	ReviewID *uuid.UUID // nil: project created or manual regenerate
	Reason   string     // the reviewer's reason, shown to the author
}

// Author writes a project's statistical workflow: the LLM drafts a script, the
// analyser dry-runs it on the current data, and the draft is saved together
// with that first run. At most 4 LLM calls and 2 runs per generation, and the
// previous version stays active on every failure path because nothing is
// written until SaveVersion.
//
// The author does not enforce the daily regeneration cap; the reviewer and the
// regenerate API check it before calling Start.
type Author struct {
	LLM      *llm.Client
	Analyser *analysis.Analyser
	Store    AuthorStore
	Bus      *events.Bus
	Budget   time.Duration
	// Insights narrates the dry run's findings so that the cards are stored
	// with the run. Nil: no insight rows are written.
	Insights *InsightBuilder
	// OnFirstRun fires once the run and its cards are stored (the reviewer
	// hooks in here); may be nil.
	OnFirstRun func(ctx context.Context, projectID, workflowID, runID uuid.UUID, res contracts.AnalysisResult)

	mu       sync.Mutex
	inFlight map[uuid.UUID]bool
}

// Start runs Generate in a goroutine with panic recovery and returns at once.
// It returns ErrBusy if this project already has a generation in flight.
func (a *Author) Start(projectID uuid.UUID, t *Trigger) error {
	if err := a.acquire(projectID); err != nil {
		return err
	}
	go func() {
		defer a.release(projectID)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("author panicked", "project", projectID, "panic", r)
				a.recordFailure(context.Background(), projectID, fmt.Sprintf("author crashed: %v", r))
			}
		}()
		if err := a.Generate(context.Background(), projectID, t); err != nil {
			slog.Warn("workflow generation failed", "project", projectID, "err", err)
		}
	}()
	return nil
}

// GenerateNow is Start's synchronous twin, for callers that want the result
// (the debug endpoint). It takes the same one-at-a-time slot, so it returns
// ErrBusy while a Start or another GenerateNow is running for the project.
func (a *Author) GenerateNow(ctx context.Context, projectID uuid.UUID, t *Trigger) error {
	if err := a.acquire(projectID); err != nil {
		return err
	}
	defer a.release(projectID)
	return a.Generate(ctx, projectID, t)
}

// WithModel returns an Author that shares this one's dependencies but asks the
// LLM for `model` when authoring. It is for experiments (the debug endpoint's
// ?model=). It keeps its own in-flight tracking, so it does not exclude runs on
// the original Author.
func (a *Author) WithModel(model string) *Author {
	c := *a.LLM
	c.Models = make(map[llm.Purpose]string, len(a.LLM.Models))
	for p, m := range a.LLM.Models {
		c.Models[p] = m
	}
	c.Models[llm.Author] = model
	return &Author{LLM: &c, Analyser: a.Analyser, Store: a.Store, Bus: a.Bus, Budget: a.Budget, Insights: a.Insights, OnFirstRun: a.OnFirstRun}
}

func (a *Author) acquire(projectID uuid.UUID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inFlight[projectID] {
		return ErrBusy
	}
	if a.inFlight == nil {
		a.inFlight = map[uuid.UUID]bool{}
	}
	a.inFlight[projectID] = true
	return nil
}

func (a *Author) release(projectID uuid.UUID) {
	a.mu.Lock()
	delete(a.inFlight, projectID)
	a.mu.Unlock()
}

// Generate writes and saves a new statistical version for the project. On
// failure it records the reason on the project (except ErrNoData) and returns
// the error.
func (a *Author) Generate(ctx context.Context, projectID uuid.UUID, t *Trigger) error {
	budget := a.Budget
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now()

	in, err := a.Store.LoadInputs(ctx, projectID)
	if err != nil {
		return a.fail(ctx, projectID, "could not load the project: "+err.Error())
	}
	watermark, ok, err := analysis.Watermark(ctx, a.Analyser.Store, projectID)
	if err != nil {
		return a.fail(ctx, projectID, "could not read the data: "+err.Error())
	}
	if !ok {
		return ErrNoData // not a failure: the run loop retries on the first source_updated
	}

	system, user := systemPrompt(), userPrompt(in, t)
	draft, err := a.draft(ctx, system, user)
	if err != nil {
		return a.fail(ctx, projectID, "author output invalid: "+err.Error())
	}

	res, err := a.Analyser.Run(ctx, projectID, draft.Script, watermark)
	var se *analysis.ScriptError
	if errors.As(err, &se) {
		// One retry: the author sees the error and its script, never results.
		draft, err = a.draft(ctx, system, user+retryBlock(draft.Script, se.ForAuthor()))
		if err != nil {
			return a.fail(ctx, projectID, "author output invalid: "+err.Error())
		}
		res, err = a.Analyser.Run(ctx, projectID, draft.Script, watermark)
		if errors.As(err, &se) {
			return a.fail(ctx, projectID, "script failed twice: "+se.Error())
		}
	}
	if err != nil {
		// The runner or store failed, not the script; no retry was spent on it.
		return a.fail(ctx, projectID, "analysis unavailable: "+err.Error())
	}

	// Narrate before saving: the run and its cards are written in one
	// transaction, so the dashboard never shows a succeeded run with no cards.
	var rows []InsightRow
	if a.Insights != nil {
		rows = a.Insights.Build(ctx, projectID, NarrationContext(in.Goal, draft.InsightPrompt, in.Sources, res), res)
	}
	wf, run, err := a.Store.SaveVersion(ctx, SaveVersion{
		ProjectID: projectID, Draft: draft, SourceIDs: in.SourceIDs, DataUpTo: watermark,
		Result: res, StartedAt: started, ReviewID: reviewID(t), Insights: rows,
	})
	if err != nil {
		return a.fail(ctx, projectID, "could not save the workflow: "+err.Error())
	}
	a.publish(events.WorkflowRegenerated, wf)
	if a.Insights != nil {
		a.publish(events.ProjectUpdated, projectID) // the dashboard refetches its cards
	}
	if a.OnFirstRun != nil {
		a.OnFirstRun(ctx, projectID, wf, run, res)
	}
	return nil
}

func (a *Author) draft(ctx context.Context, system, user string) (contracts.WorkflowDraft, error) {
	return llm.Generate(ctx, a.LLM, llm.Request{Purpose: llm.Author, System: system, User: user},
		contracts.WorkflowDraft.Validate)
}

func reviewID(t *Trigger) *uuid.UUID {
	if t == nil {
		return nil
	}
	return t.ReviewID
}

func (a *Author) fail(ctx context.Context, projectID uuid.UUID, msg string) error {
	a.recordFailure(ctx, projectID, msg)
	return errors.New(msg)
}

func (a *Author) recordFailure(ctx context.Context, projectID uuid.UUID, msg string) {
	// The generation budget may be spent; the failure must still be recorded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.Store.SetProjectError(ctx, projectID, msg); err != nil {
		slog.Error("could not record generation failure", "project", projectID, "err", err)
	}
	a.publish(events.ProjectUpdated, projectID)
}

func (a *Author) publish(typ string, id uuid.UUID) {
	if a.Bus != nil {
		a.Bus.Publish(events.Event{Type: typ, ID: id.String()})
	}
}

// Generating reports whether a statistical workflow is being written for the
// project right now.
func (a *Author) Generating(projectID uuid.UUID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inFlight[projectID]
}
