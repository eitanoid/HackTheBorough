package investigate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/insightrows"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/pyrunner"
)

var (
	ErrBusy   = errors.New("an investigation is already being generated for this project")
	ErrNoData = errors.New("project has no records yet")
)

const (
	defaultBudget   = 8 * time.Minute
	defaultMaxSteps = 12
	defaultTimeout  = 30 * time.Second

	// Automatic starts: at most this many failed attempts per project per
	// process, with these waits after the first and second failure.
	maxAutoAttempts = 3
	firstRetryWait  = 10 * time.Minute
	secondRetryWait = 30 * time.Minute
)

// Trigger says why a regeneration was started. Reason is for the prompt
// section on previous findings, which is not built yet (see Seams).
type Trigger struct {
	ReviewID *uuid.UUID
	Reason   string
}

// Investigator writes a project's investigation workflow: the model explores
// the data with describe_source and run_python, every call is recorded, and
// the cleaned report is saved with its steps and a first run built from the
// recorded outputs. Nothing is written until SaveVersion, so the previous
// version stays active on every failure path.
type Investigator struct {
	LLM    *llm.Client
	Runner pyrunner.Runner
	Store  Store
	Inputs *InputBuilder
	Bus    *events.Bus
	Chart  ChartBuilder // nil: no charts

	Budget   time.Duration // the whole generation; default 8 min
	MaxSteps int           // recorded tool calls; default 12
	Timeout  time.Duration // per run_python call; default 30 s

	// Now is for tests.
	Now func() time.Time

	mu       sync.Mutex
	inFlight map[uuid.UUID]bool
	attempts map[uuid.UUID]attempt
}

type attempt struct {
	failures int
	lastFail time.Time
}

func (i *Investigator) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

// Start runs Generate in a goroutine with panic recovery and returns at once.
// It returns ErrBusy if this project already has a generation in flight.
func (i *Investigator) Start(projectID uuid.UUID, t *Trigger) error {
	return i.start(projectID, t, false)
}

// StartIfNone is the automatic trigger. It does nothing if the project has
// ever had an investigation workflow (any version): the author's OnFirstRun
// hook also fires on statistical regenerations. It also backs off after
// failures (10 min, then 30 min, then no more automatic attempts until the
// process restarts), so a broken investigation doesn't spend LLM calls on
// every sweep.
func (i *Investigator) StartIfNone(ctx context.Context, projectID uuid.UUID) error {
	has, err := i.Store.HasInvestigation(ctx, projectID)
	if err != nil {
		return fmt.Errorf("check for an investigation: %w", err)
	}
	if has || !i.mayRetry(projectID) {
		return nil
	}
	if err := i.start(projectID, nil, true); err != nil && !errors.Is(err, ErrBusy) {
		return err
	}
	return nil
}

func (i *Investigator) mayRetry(projectID uuid.UUID) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	a := i.attempts[projectID]
	switch {
	case a.failures == 0:
		return true
	case a.failures >= maxAutoAttempts:
		return false
	case a.failures == 1:
		return !i.now().Before(a.lastFail.Add(firstRetryWait))
	default:
		return !i.now().Before(a.lastFail.Add(secondRetryWait))
	}
}

func (i *Investigator) start(projectID uuid.UUID, t *Trigger, auto bool) error {
	if err := i.acquire(projectID); err != nil {
		return err
	}
	go func() {
		defer i.release(projectID)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("investigator panicked", "project", projectID, "panic", r)
				i.recordFailure(context.Background(), projectID, fmt.Sprintf("investigator crashed: %v", r))
				if auto {
					i.noteFailure(projectID)
				}
			}
		}()
		err := i.Generate(context.Background(), projectID, t)
		if err != nil {
			slog.Warn("investigation generation failed", "project", projectID, "err", err)
			if auto && !errors.Is(err, ErrNoData) {
				i.noteFailure(projectID)
			}
		}
	}()
	return nil
}

func (i *Investigator) noteFailure(projectID uuid.UUID) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.attempts == nil {
		i.attempts = map[uuid.UUID]attempt{}
	}
	a := i.attempts[projectID]
	i.attempts[projectID] = attempt{failures: a.failures + 1, lastFail: i.now()}
}

func (i *Investigator) acquire(projectID uuid.UUID) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.inFlight[projectID] {
		return ErrBusy
	}
	if i.inFlight == nil {
		i.inFlight = map[uuid.UUID]bool{}
	}
	i.inFlight[projectID] = true
	return nil
}

func (i *Investigator) release(projectID uuid.UUID) {
	i.mu.Lock()
	delete(i.inFlight, projectID)
	i.mu.Unlock()
}

// Generate investigates the project and saves a new version. On failure it
// records the reason on the project (except ErrNoData) and returns the error.
// It does not take the one-at-a-time slot; Start does.
func (i *Investigator) Generate(ctx context.Context, projectID uuid.UUID, t *Trigger) error {
	budget := i.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now()

	in, err := i.Store.LoadInputs(ctx, projectID)
	if err != nil {
		return i.fail(ctx, projectID, "could not load the project: "+err.Error())
	}
	dataUpTo, ok, err := analysis.Watermark(ctx, i.Inputs.Store, projectID)
	if err != nil {
		return i.fail(ctx, projectID, "could not read the data: "+err.Error())
	}
	if !ok {
		return ErrNoData // not a failure: the run loop retries once data arrives
	}
	input, err := i.Inputs.Build(ctx, projectID, dataUpTo)
	if err != nil {
		return i.fail(ctx, projectID, "could not build the input: "+err.Error())
	}

	maxSteps, timeout := i.MaxSteps, i.Timeout
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	rec := &recorder{in: input, run: i.Runner, timeout: timeout, maxSteps: maxSteps}
	brief := userPrompt(in)
	report, err := llm.RunTools(ctx, i.LLM,
		llm.Request{Purpose: llm.Investigator, System: systemPrompt(), User: brief},
		rec.tools(), validateReport)
	if err != nil {
		return i.fail(ctx, projectID, "investigator output invalid: "+err.Error())
	}

	steps := rec.Steps()
	report = Clean(report, steps)
	replay := FirstRun(report, steps, dataUpTo)
	var reviewID *uuid.UUID
	if t != nil {
		reviewID = t.ReviewID
	}
	saved, err := i.Store.SaveVersion(ctx, SaveVersion{
		ProjectID: projectID, Report: report, Brief: brief, SourceIDs: in.SourceIDs, Steps: steps,
		DataUpTo: dataUpTo, StartedAt: started, Replay: replay, ReviewID: reviewID,
		Cards: func(version int) []insightrows.InsightRow { return Cards(report, version, replay, nil, i.Chart) },
	})
	if err != nil {
		return i.fail(ctx, projectID, "could not save the investigation: "+err.Error())
	}
	slog.Info("investigation generated", "project", projectID, "workflow", saved.WorkflowID, "version", saved.Version,
		"steps", len(steps), "findings", len(report.Findings))
	i.publish(events.WorkflowRegenerated, saved.WorkflowID)
	i.publish(events.ProjectUpdated, projectID)
	return nil
}

// validateReport is structural only: Clean handles unresolved claims, so one
// bad path doesn't throw away a twelve-step investigation. A report with no
// findings is allowed.
func validateReport(r contracts.InvestigationReport) error {
	if strings.TrimSpace(r.Rationale) == "" {
		return errors.New("rationale is empty")
	}
	for n, f := range r.Findings {
		if strings.TrimSpace(f.Headline) == "" || strings.TrimSpace(f.Explanation) == "" {
			return fmt.Errorf("findings[%d] needs a headline and an explanation", n)
		}
		if len(f.Claims) > contracts.MaxClaimsPerFinding {
			return fmt.Errorf("findings[%d] has %d claims; the limit is %d", n, len(f.Claims), contracts.MaxClaimsPerFinding)
		}
	}
	return nil
}

func (i *Investigator) fail(ctx context.Context, projectID uuid.UUID, msg string) error {
	i.recordFailure(ctx, projectID, msg)
	return errors.New(msg)
}

func (i *Investigator) recordFailure(ctx context.Context, projectID uuid.UUID, msg string) {
	// The generation budget may be spent; the failure must still be recorded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := i.Store.SetProjectError(ctx, projectID, ErrorPrefix+msg); err != nil {
		slog.Error("could not record investigation failure", "project", projectID, "err", err)
	}
	i.publish(events.ProjectUpdated, projectID)
}

func (i *Investigator) publish(typ string, id uuid.UUID) {
	if i.Bus != nil {
		i.Bus.Publish(events.Event{Type: typ, ID: id.String()})
	}
}
