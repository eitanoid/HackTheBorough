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
	ErrRunnerNotRunning            = errors.New("workflow runner is not running")
	ErrNoActiveStatisticalWorkflow = errors.New("project has no active statistical workflow")
)

// StatisticalWorkflow is the active statistical artifact to execute.
type StatisticalWorkflow struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	Script        string
	InsightPrompt string
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
	ProjectContext(ctx context.Context, projectID uuid.UUID) (RunProjectContext, error)
	State(ctx context.Context, workflowID uuid.UUID) (RunState, error)
	StartRun(ctx context.Context, workflowID uuid.UUID, dataUpTo, startedAt time.Time) (uuid.UUID, error)
	SucceedRun(ctx context.Context, projectID, runID uuid.UUID, result contracts.AnalysisResult, insights []InsightRow, finishedAt time.Time) error
	FailRun(ctx context.Context, runID uuid.UUID, message string, finishedAt time.Time) error
}

// Runner schedules and executes active statistical workflows. Workflow
// generation is delegated to Author; investigation and review are intentionally
// outside this runner's current scope.
type Runner struct {
	Store    RunStore
	Analyser StatisticalAnalyser
	Insights *InsightBuilder
	Author   *Author
	Bus      *events.Bus

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

// ForceRun schedules the active statistical workflow even when its watermark
// matches the latest successful run or a failure is in its retry cooldown.
// It still honors the per-workflow in-flight lock. The caller can poll GetProject
// for the resulting run and report.
func (r *Runner) ForceRun(ctx context.Context, projectID uuid.UUID) error {
	if r.Store == nil {
		return ErrRunnerNotRunning
	}
	workflow, err := r.Store.ActiveStatisticalWorkflow(ctx, projectID)
	if err != nil {
		return err
	}
	if workflow == nil {
		return ErrNoActiveStatisticalWorkflow
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
	if !r.acquire(workflow.ID) {
		return ErrBusy
	}
	go func() {
		defer r.release(workflow.ID)
		r.runWorkflow(serviceCtx, *workflow, watermark)
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

	state, err := r.Store.State(ctx, workflow.ID)
	if err != nil {
		slog.Error("workflow runner load run state", "workflow", workflow.ID, "err", err)
		return
	}
	if state.SuccessfulDataUpTo != nil && !watermark.After(*state.SuccessfulDataUpTo) {
		return
	}
	if state.LastStatus == "failed" && state.LastFinishedAt != nil && time.Now().Before(state.LastFinishedAt.Add(r.retryDelay(state.ConsecutiveFailures))) {
		return
	}

	r.runWorkflow(ctx, *workflow, watermark)
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
		r.failRun(workflow, runID, errors.New("analysis is unavailable"))
		return
	}
	res, err := r.Analyser.Run(ctx, workflow.ProjectID, workflow.Script, watermark)
	if err != nil {
		r.failRun(workflow, runID, err)
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
		r.failRun(workflow, runID, fmt.Errorf("save run: %w", err))
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

func (r *Runner) failRun(workflow StatisticalWorkflow, runID uuid.UUID, err error) {
	slog.Warn("statistical workflow run failed", "workflow", workflow.ID, "run", runID, "err", err)
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
