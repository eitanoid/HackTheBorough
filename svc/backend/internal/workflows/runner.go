package workflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/events"
)

const (
	defaultRunSweep       = 5 * time.Minute
	defaultRunDebounce    = 30 * time.Second
	defaultFailureBackoff = 30 * time.Second
	maxFailureBackoff     = 5 * time.Minute
)

var (
	ErrRunnerNotRunning              = errors.New("workflow runner is not running")
	ErrNoActiveStatisticalWorkflow   = errors.New("project has no active statistical workflow")
	ErrNoActiveInvestigationWorkflow = errors.New("project has no active investigation workflow")
)

// Workflow kinds, as stored in workflows.kind.
const (
	KindStatistical   = "statistical"
	KindInvestigation = "investigation"
)

// StatisticalWorkflow is the active statistical artifact to execute.
type StatisticalWorkflow struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	Script        string
	InsightPrompt string
}

// InvestigationWorkflow is the active investigation artifact. Its recorded
// steps and report are loaded by the replayer.
type InvestigationWorkflow struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
}

// workflowRef is what failRun needs to know about either kind.
type workflowRef struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Kind      string
}

// InvestigationReplayer re-executes an investigation's cited steps at a new
// watermark (investigate.Replayer). It makes no LLM call.
type InvestigationReplayer interface {
	Replay(ctx context.Context, workflowID uuid.UUID, dataUpTo time.Time) (contracts.ReplayResult, []InsightRow, error)
}

// InvestigationStarter starts the investigator once per project
// (investigate.Investigator.StartIfNone). It must do nothing when the project
// already has an investigation workflow, and it must back off after failures.
type InvestigationStarter interface {
	StartIfNone(ctx context.Context, projectID uuid.UUID) error
}

// RunState describes the latest successful run and the current retry state.
type RunState struct {
	SuccessfulDataUpTo  *time.Time
	LastStatus          string
	LastFinishedAt      *time.Time
	ConsecutiveFailures int
}

// RunProjectContext contains the narrative metadata needed to build cards.
type RunProjectContext struct {
	Goal    string
	Sources []AuthorSource
}

type StatisticalAnalyser interface {
	Run(ctx context.Context, projectID uuid.UUID, script string, dataUpTo time.Time) (contracts.AnalysisResult, error)
}

// RunStore persists scheduler state and statistical run results.
type RunStore interface {
	ProjectIDs(ctx context.Context) ([]uuid.UUID, error)
	ProjectsForSource(ctx context.Context, sourceID uuid.UUID) ([]uuid.UUID, error)
	Watermark(ctx context.Context, projectID uuid.UUID) (time.Time, bool, error)
	ActiveStatisticalWorkflow(ctx context.Context, projectID uuid.UUID) (*StatisticalWorkflow, error)
	// ActiveInvestigationWorkflow returns nil when the project has none.
	ActiveInvestigationWorkflow(ctx context.Context, projectID uuid.UUID) (*InvestigationWorkflow, error)
	ProjectContext(ctx context.Context, projectID uuid.UUID) (RunProjectContext, error)
	State(ctx context.Context, workflowID uuid.UUID) (RunState, error)
	StartRun(ctx context.Context, workflowID uuid.UUID, dataUpTo, startedAt time.Time) (uuid.UUID, error)
	SucceedRun(ctx context.Context, projectID, runID uuid.UUID, result contracts.AnalysisResult, insights []InsightRow, finishedAt time.Time) error
	// SucceedReplay writes an investigation run's result and its cards in one
	// transaction.
	SucceedReplay(ctx context.Context, projectID, runID uuid.UUID, result contracts.ReplayResult, insights []InsightRow, finishedAt time.Time) error
	FailRun(ctx context.Context, runID uuid.UUID, message string, finishedAt time.Time) error
}

// Runner schedules and executes active statistical workflows and replays active
// investigation workflows. Workflow generation is delegated to Author and the
// Investigator; review is outside this runner's scope.
type Runner struct {
	Store    RunStore
	Analyser StatisticalAnalyser
	Insights *InsightBuilder
	Author   *Author
	Bus      *events.Bus

	// Replayer and Investigator are nil when investigations are disabled
	// (no LLM or no Python runner).
	Replayer     InvestigationReplayer
	Investigator InvestigationStarter

	SweepInterval  time.Duration
	Debounce       time.Duration
	FailureBackoff time.Duration

	mu       sync.Mutex
	inFlight map[uuid.UUID]bool
	sweepMu  sync.Mutex

	serviceMu  sync.RWMutex
	serviceCtx context.Context

	debounceMu sync.Mutex
	pending    map[uuid.UUID]pendingRun
}

type pendingRun struct {
	timer      *time.Timer
	generation uint64
}

// Run does an initial catch-up sweep, then responds to source updates and a
// periodic sweep. The sweep is a recovery mechanism for missed in-process
// events and service restarts; events merely reduce update latency.
func (r *Runner) Run(ctx context.Context) {
	if r.Store == nil {
		slog.Error("workflow runner has no store")
		return
	}
	r.SetServiceContext(ctx)
	defer r.SetServiceContext(nil)
	interval := r.SweepInterval
	if interval <= 0 {
		interval = defaultRunSweep
	}
	debounce := r.Debounce
	if debounce <= 0 {
		debounce = defaultRunDebounce
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var eventCh <-chan events.Event
	var unsubscribe func()
	if r.Bus != nil {
		eventCh, unsubscribe = r.Bus.Subscribe()
		defer unsubscribe()
	}
	defer r.stopDebounces()

	r.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			go r.Sweep(ctx)
		case event, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			if event.Type == events.SourceUpdated {
				go r.sourceUpdated(ctx, event.ID, debounce)
			}
		}
	}
}

// SetServiceContext sets the lifetime context used for asynchronous manual
// runs. The server sets it before exposing the RPC handler; Run also sets it
// when the scheduler starts.
func (r *Runner) SetServiceContext(ctx context.Context) {
	r.serviceMu.Lock()
	r.serviceCtx = ctx
	r.serviceMu.Unlock()
}

// ForceRun schedules the project's active workflow of the given kind
// (KindStatistical or KindInvestigation) even when its watermark matches the
// latest successful run or a failure is in its retry cooldown. It still honors
// the per-workflow in-flight lock. The caller can poll GetProject for the
// resulting run and report. Forcing an investigation replays its recorded
// steps; it makes no LLM call.
func (r *Runner) ForceRun(ctx context.Context, projectID uuid.UUID, kind string) error {
	if r.Store == nil {
		return ErrRunnerNotRunning
	}
	var run func(ctx context.Context, watermark time.Time)
	var workflowID uuid.UUID
	switch kind {
	case KindStatistical:
		workflow, err := r.Store.ActiveStatisticalWorkflow(ctx, projectID)
		if err != nil {
			return err
		}
		if workflow == nil {
			return ErrNoActiveStatisticalWorkflow
		}
		workflowID = workflow.ID
		run = func(ctx context.Context, watermark time.Time) { r.runWorkflow(ctx, *workflow, watermark) }
	case KindInvestigation:
		workflow, err := r.Store.ActiveInvestigationWorkflow(ctx, projectID)
		if err != nil {
			return err
		}
		if workflow == nil || r.Replayer == nil {
			return ErrNoActiveInvestigationWorkflow
		}
		workflowID = workflow.ID
		run = func(ctx context.Context, watermark time.Time) { r.replayWorkflow(ctx, *workflow, watermark) }
	default:
		return fmt.Errorf("unknown workflow kind %q", kind)
	}
	watermark, hasData, err := r.Store.Watermark(ctx, projectID)
	if err != nil {
		return err
	}
	if !hasData {
		return ErrNoData
	}
	r.serviceMu.RLock()
	serviceCtx := r.serviceCtx
	r.serviceMu.RUnlock()
	if serviceCtx == nil || serviceCtx.Err() != nil {
		return ErrRunnerNotRunning
	}
	if !r.acquire(workflowID) {
		return ErrBusy
	}
	go func() {
		defer r.release(workflowID)
		run(serviceCtx, watermark)
	}()
	return nil
}

// Sweep checks every project. It is safe for overlapping sweeps: workflow run
// locks prevent duplicate execution and Author independently locks generation.
func (r *Runner) Sweep(ctx context.Context) {
	if r.Store == nil || ctx.Err() != nil {
		return
	}
	if !r.sweepMu.TryLock() {
		return
	}
	defer r.sweepMu.Unlock()
	projects, err := r.Store.ProjectIDs(ctx)
	if err != nil {
		slog.Error("workflow runner list projects", "err", err)
		return
	}
	for _, projectID := range projects {
		if ctx.Err() != nil {
			return
		}
		r.runProject(ctx, projectID)
	}
}

func (r *Runner) sourceUpdated(ctx context.Context, rawID string, debounce time.Duration) {
	sourceID, err := uuid.Parse(rawID)
	if err != nil {
		slog.Warn("workflow runner received invalid source id", "source_id", rawID)
		return
	}
	projects, err := r.Store.ProjectsForSource(ctx, sourceID)
	if err != nil {
		slog.Error("workflow runner find source projects", "source_id", sourceID, "err", err)
		return
	}
	for _, projectID := range projects {
		r.debounceProject(ctx, projectID, debounce)
	}
}

func (r *Runner) debounceProject(ctx context.Context, projectID uuid.UUID, delay time.Duration) {
	r.debounceMu.Lock()
	if r.pending == nil {
		r.pending = make(map[uuid.UUID]pendingRun)
	}
	previous := r.pending[projectID]
	if previous.timer != nil {
		previous.timer.Stop()
	}
	generation := previous.generation + 1
	timer := time.AfterFunc(delay, func() {
		r.debounceMu.Lock()
		current, ok := r.pending[projectID]
		if !ok || current.generation != generation {
			r.debounceMu.Unlock()
			return
		}
		delete(r.pending, projectID)
		r.debounceMu.Unlock()
		r.runProject(ctx, projectID)
	})
	r.pending[projectID] = pendingRun{timer: timer, generation: generation}
	r.debounceMu.Unlock()
}

func (r *Runner) stopDebounces() {
	r.debounceMu.Lock()
	defer r.debounceMu.Unlock()
	for id, pending := range r.pending {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		delete(r.pending, id)
	}
}

func (r *Runner) runProject(ctx context.Context, projectID uuid.UUID) {
	if ctx.Err() != nil {
		return
	}
	watermark, hasData, err := r.Store.Watermark(ctx, projectID)
	if err != nil {
		slog.Error("workflow runner read watermark", "project", projectID, "err", err)
		return
	}
	if !hasData {
		return
	}
	// The two kinds are independent: a failure of one never blocks the other.
	r.runStatistical(ctx, projectID, watermark)
	r.runInvestigation(ctx, projectID, watermark)
}

// due reports whether a workflow should run at this watermark: not already
// run there, and not inside the retry cooldown after a failure.
func (r *Runner) due(ctx context.Context, workflowID uuid.UUID, watermark time.Time) bool {
	state, err := r.Store.State(ctx, workflowID)
	if err != nil {
		slog.Error("workflow runner load run state", "workflow", workflowID, "err", err)
		return false
	}
	if state.SuccessfulDataUpTo != nil && !watermark.After(*state.SuccessfulDataUpTo) {
		return false
	}
	if state.LastStatus == "failed" && state.LastFinishedAt != nil && time.Now().Before(state.LastFinishedAt.Add(r.retryDelay(state.ConsecutiveFailures))) {
		return false
	}
	return true
}

func (r *Runner) runStatistical(ctx context.Context, projectID uuid.UUID, watermark time.Time) {
	workflow, err := r.Store.ActiveStatisticalWorkflow(ctx, projectID)
	if err != nil {
		slog.Error("workflow runner load active workflow", "project", projectID, "err", err)
		return
	}
	if workflow == nil {
		if r.Author != nil {
			if err := r.Author.Start(projectID, nil); err != nil && !errors.Is(err, ErrBusy) {
				slog.Warn("workflow runner start author", "project", projectID, "err", err)
			}
		}
		return
	}

	if !r.acquire(workflow.ID) {
		return
	}
	defer r.release(workflow.ID)
	if !r.due(ctx, workflow.ID, watermark) {
		return
	}
	r.runWorkflow(ctx, *workflow, watermark)
}

func (r *Runner) runInvestigation(ctx context.Context, projectID uuid.UUID, watermark time.Time) {
	if r.Replayer == nil && r.Investigator == nil {
		return
	}
	workflow, err := r.Store.ActiveInvestigationWorkflow(ctx, projectID)
	if err != nil {
		slog.Error("workflow runner load active investigation", "project", projectID, "err", err)
		return
	}
	if workflow == nil {
		r.startInvestigation(ctx, projectID)
		return
	}
	if r.Replayer == nil {
		return
	}

	if !r.acquire(workflow.ID) {
		return
	}
	defer r.release(workflow.ID)
	if !r.due(ctx, workflow.ID, watermark) {
		return
	}
	r.replayWorkflow(ctx, *workflow, watermark)
}

// startInvestigation is the sweep fallback for the author's OnFirstRun hook:
// it covers restarts, projects created before investigations existed, and a
// lost hook call. The investigator needs a succeeded statistical run for its
// leads, and StartIfNone's own backoff stops it from retrying on every sweep.
func (r *Runner) startInvestigation(ctx context.Context, projectID uuid.UUID) {
	if r.Investigator == nil {
		return
	}
	stat, err := r.Store.ActiveStatisticalWorkflow(ctx, projectID)
	if err != nil || stat == nil {
		return
	}
	state, err := r.Store.State(ctx, stat.ID)
	if err != nil || state.SuccessfulDataUpTo == nil {
		return
	}
	if err := r.Investigator.StartIfNone(ctx, projectID); err != nil {
		slog.Warn("workflow runner start investigator", "project", projectID, "err", err)
	}
}

func (r *Runner) runWorkflow(ctx context.Context, workflow StatisticalWorkflow, watermark time.Time) {
	startedAt := time.Now().UTC()
	runID, err := r.Store.StartRun(ctx, workflow.ID, watermark, startedAt)
	if err != nil {
		slog.Error("workflow runner create run", "workflow", workflow.ID, "err", err)
		r.publish(events.RunFailed, workflow.ProjectID)
		return
	}
	r.publish(events.ProjectUpdated, workflow.ProjectID)
	if r.Analyser == nil {
		r.failRun(workflowRef{ID: workflow.ID, ProjectID: workflow.ProjectID, Kind: KindStatistical}, runID, errors.New("analysis is unavailable"))
		return
	}
	res, err := r.Analyser.Run(ctx, workflow.ProjectID, workflow.Script, watermark)
	if err != nil {
		r.failRun(workflowRef{ID: workflow.ID, ProjectID: workflow.ProjectID, Kind: KindStatistical}, runID, err)
		return
	}

	var rows []InsightRow
	if r.Insights != nil {
		project, loadErr := r.Store.ProjectContext(ctx, workflow.ProjectID)
		if loadErr != nil {
			slog.Warn("workflow runner load narration context", "project", workflow.ProjectID, "err", loadErr)
			project = RunProjectContext{}
		}
		rows = r.Insights.Build(ctx, workflow.ProjectID, NarrationContext(project.Goal, workflow.InsightPrompt, project.Sources, res), res)
	}
	if err := r.Store.SucceedRun(ctx, workflow.ProjectID, runID, res, rows, time.Now().UTC()); err != nil {
		r.failRun(workflowRef{ID: workflow.ID, ProjectID: workflow.ProjectID, Kind: KindStatistical}, runID, fmt.Errorf("save run: %w", err))
		return
	}
	recordCount := 0
	for _, source := range res.Health.Sources {
		recordCount += source.Records
	}
	slog.Info("statistical workflow run completed",
		"project_id", workflow.ProjectID,
		"workflow_id", workflow.ID,
		"run_id", runID,
		"records", recordCount,
		"series", len(res.Health.Series),
		"tests_run", res.Funnel["tests_run"],
		"findings", len(res.Findings),
		"insights_saved", len(rows),
	)
	if len(res.Findings) > 0 && len(rows) == 0 {
		slog.Warn("workflow run produced findings but no insights were saved",
			"project_id", workflow.ProjectID,
			"workflow_id", workflow.ID,
			"run_id", runID,
			"findings", len(res.Findings),
		)
	}
	r.publish(events.ProjectUpdated, workflow.ProjectID)
}

func (r *Runner) failRun(workflow workflowRef, runID uuid.UUID, err error) {
	slog.Warn(workflow.Kind+" workflow run failed", "workflow", workflow.ID, "run", runID, "err", err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if saveErr := r.Store.FailRun(ctx, runID, err.Error(), time.Now().UTC()); saveErr != nil {
		slog.Error("workflow runner record failure", "run", runID, "err", saveErr)
	}
	r.publish(events.RunFailed, workflow.ProjectID)
}

func (r *Runner) retryDelay(consecutiveFailures int) time.Duration {
	base := r.FailureBackoff
	if base <= 0 {
		base = defaultFailureBackoff
	}
	delay := base
	for i := 1; i < consecutiveFailures && delay < maxFailureBackoff; i++ {
		delay *= 2
	}
	if delay > maxFailureBackoff {
		return maxFailureBackoff
	}
	return delay
}

func (r *Runner) acquire(workflowID uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == nil {
		r.inFlight = make(map[uuid.UUID]bool)
	}
	if r.inFlight[workflowID] {
		return false
	}
	r.inFlight[workflowID] = true
	return true
}

func (r *Runner) release(workflowID uuid.UUID) {
	r.mu.Lock()
	delete(r.inFlight, workflowID)
	r.mu.Unlock()
}

func (r *Runner) publish(eventType string, projectID uuid.UUID) {
	if r.Bus != nil {
		r.Bus.Publish(events.Event{Type: eventType, ID: projectID.String()})
	}
}

// replayWorkflow replays an investigation's cited steps at the watermark and
// stores the run with its cards. The replayer makes no LLM call. A step that
// fails on the new data degrades its own card; only a runner failure, or every
// cited step failing, fails the run.
func (r *Runner) replayWorkflow(ctx context.Context, workflow InvestigationWorkflow, watermark time.Time) {
	ref := workflowRef{ID: workflow.ID, ProjectID: workflow.ProjectID, Kind: KindInvestigation}
	startedAt := time.Now().UTC()
	runID, err := r.Store.StartRun(ctx, workflow.ID, watermark, startedAt)
	if err != nil {
		slog.Error("workflow runner create run", "workflow", workflow.ID, "err", err)
		r.publish(events.RunFailed, workflow.ProjectID)
		return
	}
	r.publish(events.ProjectUpdated, workflow.ProjectID)
	if r.Replayer == nil {
		r.failRun(ref, runID, errors.New("investigation replay is unavailable"))
		return
	}
	result, rows, err := r.Replayer.Replay(ctx, workflow.ID, watermark)
	if err != nil {
		r.failRun(ref, runID, err)
		return
	}
	if err := r.Store.SucceedReplay(ctx, workflow.ProjectID, runID, result, rows, time.Now().UTC()); err != nil {
		r.failRun(ref, runID, fmt.Errorf("save run: %w", err))
		return
	}
	carried := 0
	for _, s := range result.Steps {
		if s.Error != nil {
			carried++
		}
	}
	slog.Info("investigation replay completed",
		"project_id", workflow.ProjectID, "workflow_id", workflow.ID, "run_id", runID,
		"steps", len(result.Steps), "carried_steps", carried, "insights_saved", len(rows))
	r.publish(events.ProjectUpdated, workflow.ProjectID)
}
