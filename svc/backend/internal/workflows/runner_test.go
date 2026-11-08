package workflows

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/contracts"
)

type fakeRunStore struct {
	mu sync.Mutex

	projectIDs     []uuid.UUID
	sourceProjects map[uuid.UUID][]uuid.UUID
	watermark      time.Time
	hasData        bool
	workflow       *StatisticalWorkflow
	state          RunState

	startCalls       int
	successCalls     int
	failCalls        int
	startedWatermark time.Time
	onSuccess        chan struct{}
}

func (s *fakeRunStore) ProjectIDs(context.Context) ([]uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.projectIDs...), nil
}

func (s *fakeRunStore) ProjectsForSource(_ context.Context, sourceID uuid.UUID) ([]uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.sourceProjects[sourceID]...), nil
}

func (s *fakeRunStore) Watermark(context.Context, uuid.UUID) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watermark, s.hasData, nil
}

func (s *fakeRunStore) ActiveStatisticalWorkflow(context.Context, uuid.UUID) (*StatisticalWorkflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workflow == nil {
		return nil, nil
	}
	wf := *s.workflow
	return &wf, nil
}

func (s *fakeRunStore) ProjectContext(context.Context, uuid.UUID) (RunProjectContext, error) {
	return RunProjectContext{}, nil
}

func (s *fakeRunStore) State(context.Context, uuid.UUID) (RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *fakeRunStore) StartRun(_ context.Context, _ uuid.UUID, watermark, _ time.Time) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startCalls++
	s.startedWatermark = watermark
	return uuid.New(), nil
}

func (s *fakeRunStore) SucceedRun(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ contracts.AnalysisResult, _ []InsightRow, finishedAt time.Time) error {
	s.mu.Lock()
	s.successCalls++
	watermark := s.startedWatermark
	s.state = RunState{SuccessfulDataUpTo: &watermark, LastStatus: "succeeded", LastFinishedAt: &finishedAt}
	ch := s.onSuccess
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *fakeRunStore) FailRun(_ context.Context, _ uuid.UUID, _ string, finishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCalls++
	failures := s.state.ConsecutiveFailures + 1
	s.state.LastStatus = "failed"
	s.state.LastFinishedAt = &finishedAt
	s.state.ConsecutiveFailures = failures
	return nil
}

type fakeStatisticalAnalyser struct {
	mu      sync.Mutex
	calls   int
	err     error
	entered chan struct{}
	resume  chan struct{}
}

func (a *fakeStatisticalAnalyser) Run(context.Context, uuid.UUID, string, time.Time) (contracts.AnalysisResult, error) {
	a.mu.Lock()
	a.calls++
	entered, resume, err := a.entered, a.resume, a.err
	a.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if resume != nil {
		<-resume
	}
	return contracts.AnalysisResult{}, err
}

func (a *fakeStatisticalAnalyser) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func newRunnerHarness() (*Runner, *fakeRunStore, *fakeStatisticalAnalyser, uuid.UUID) {
	projectID, workflowID := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	store := &fakeRunStore{
		projectIDs: []uuid.UUID{projectID},
		watermark:  now,
		hasData:    true,
		workflow:   &StatisticalWorkflow{ID: workflowID, ProjectID: projectID, Script: "result = {}"},
	}
	analyser := &fakeStatisticalAnalyser{}
	runner := &Runner{Store: store, Analyser: analyser}
	return runner, store, analyser, projectID
}

func TestRunnerRunsOnlyWhenWatermarkAdvances(t *testing.T) {
	runner, store, analyser, projectID := newRunnerHarness()
	ctx := context.Background()

	runner.runProject(ctx, projectID)
	runner.runProject(ctx, projectID)
	if got := analyser.callCount(); got != 1 {
		t.Fatalf("analysis calls after unchanged watermark = %d, want 1", got)
	}
	if store.startCalls != 1 || store.successCalls != 1 {
		t.Fatalf("runs started/succeeded = %d/%d, want 1/1", store.startCalls, store.successCalls)
	}

	store.mu.Lock()
	store.watermark = store.watermark.Add(time.Minute)
	store.mu.Unlock()
	runner.runProject(ctx, projectID)
	if got := analyser.callCount(); got != 2 {
		t.Fatalf("analysis calls after watermark advance = %d, want 2", got)
	}
}

func TestRunnerSkipsProjectsWithoutData(t *testing.T) {
	runner, store, analyser, projectID := newRunnerHarness()
	store.hasData = false
	runner.runProject(context.Background(), projectID)
	if got := analyser.callCount(); got != 0 || store.startCalls != 0 {
		t.Fatalf("analysis calls/run rows = %d/%d, want 0/0", got, store.startCalls)
	}
}

func TestRunnerDoesInitialSweepAndPeriodicSweep(t *testing.T) {
	runner, store, _, _ := newRunnerHarness()
	store.onSuccess = make(chan struct{}, 2)
	runner.SweepInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	defer cancel()

	select {
	case <-store.onSuccess: // initial sweep
	case <-time.After(time.Second):
		t.Fatal("runner did not perform its initial sweep")
	}
	store.mu.Lock()
	store.watermark = store.watermark.Add(time.Minute)
	store.mu.Unlock()
	select {
	case <-store.onSuccess: // ticker sweep
	case <-time.After(time.Second):
		t.Fatal("runner did not process the advanced watermark on a ticker sweep")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after context cancellation")
	}
}

func TestRunnerFailureBackoffIsBoundedAndPersistentInRunState(t *testing.T) {
	runner, store, analyser, projectID := newRunnerHarness()
	analyser.err = errors.New("script runner unavailable")
	finished := time.Now().UTC()
	store.state = RunState{LastStatus: "failed", LastFinishedAt: &finished, ConsecutiveFailures: 1}
	runner.runProject(context.Background(), projectID)
	if store.startCalls != 0 {
		t.Fatalf("started during retry cooldown = %d, want 0", store.startCalls)
	}

	finished = time.Now().Add(-time.Minute)
	store.state.LastFinishedAt = &finished
	runner.runProject(context.Background(), projectID)
	if store.startCalls != 1 || store.failCalls != 1 {
		t.Fatalf("started/failed after cooldown = %d/%d, want 1/1", store.startCalls, store.failCalls)
	}
	if got := runner.retryDelay(100); got != maxFailureBackoff {
		t.Fatalf("retry delay after many failures = %s, want cap %s", got, maxFailureBackoff)
	}
}

func TestRunnerPreventsOverlappingRunsPerWorkflow(t *testing.T) {
	runner, store, analyser, projectID := newRunnerHarness()
	analyser.entered = make(chan struct{}, 1)
	analyser.resume = make(chan struct{})
	done := make(chan struct{})
	go func() {
		runner.runProject(context.Background(), projectID)
		close(done)
	}()
	select {
	case <-analyser.entered:
	case <-time.After(time.Second):
		t.Fatal("first run did not reach the analyser")
	}
	runner.runProject(context.Background(), projectID)
	if store.startCalls != 1 {
		t.Fatalf("overlapping run rows = %d, want 1", store.startCalls)
	}
	close(analyser.resume)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first run did not finish")
	}
}

func TestForceRunBypassesWatermarkAndRetryCooldown(t *testing.T) {
	runner, store, analyser, projectID := newRunnerHarness()
	runner.SetServiceContext(context.Background())
	watermark := store.watermark
	finished := time.Now().UTC()
	store.state = RunState{
		SuccessfulDataUpTo:  &watermark,
		LastStatus:          "failed",
		LastFinishedAt:      &finished,
		ConsecutiveFailures: 3,
	}
	store.onSuccess = make(chan struct{}, 1)

	if err := runner.ForceRun(context.Background(), projectID); err != nil {
		t.Fatalf("ForceRun: %v", err)
	}
	select {
	case <-store.onSuccess:
	case <-time.After(time.Second):
		t.Fatal("forced run did not complete")
	}
	if got := analyser.callCount(); got != 1 {
		t.Fatalf("analysis calls = %d, want 1", got)
	}
	if store.startCalls != 1 || store.successCalls != 1 {
		t.Fatalf("runs started/succeeded = %d/%d, want 1/1", store.startCalls, store.successCalls)
	}
}

func TestForceRunRejectsWorkflowAlreadyInFlight(t *testing.T) {
	runner, _, analyser, projectID := newRunnerHarness()
	runner.SetServiceContext(context.Background())
	analyser.entered = make(chan struct{}, 1)
	analyser.resume = make(chan struct{})
	done := make(chan struct{})
	go func() {
		runner.runProject(context.Background(), projectID)
		close(done)
	}()
	select {
	case <-analyser.entered:
	case <-time.After(time.Second):
		t.Fatal("scheduled run did not start")
	}
	if err := runner.ForceRun(context.Background(), projectID); !errors.Is(err, ErrBusy) {
		t.Fatalf("ForceRun error = %v, want ErrBusy", err)
	}
	close(analyser.resume)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduled run did not finish")
	}
}

func TestRunnerDebouncesSourceUpdatesPerProject(t *testing.T) {
	runner, store, _, projectID := newRunnerHarness()
	sourceID := uuid.New()
	store.sourceProjects = map[uuid.UUID][]uuid.UUID{sourceID: {projectID}}
	store.onSuccess = make(chan struct{}, 2)

	runner.sourceUpdated(context.Background(), sourceID.String(), 20*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	runner.sourceUpdated(context.Background(), sourceID.String(), 20*time.Millisecond)
	select {
	case <-store.onSuccess:
	case <-time.After(time.Second):
		t.Fatal("debounced source update did not run the workflow")
	}
	select {
	case <-store.onSuccess:
		t.Fatal("source updates were not coalesced")
	case <-time.After(50 * time.Millisecond):
	}
	runner.stopDebounces()
}
