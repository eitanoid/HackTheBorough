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

type fakeReplayer struct {
	mu    sync.Mutex
	calls int
	at    []time.Time
	err   error
}

func (f *fakeReplayer) Replay(_ context.Context, _ uuid.UUID, dataUpTo time.Time) (contracts.ReplayResult, []InsightRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.at = append(f.at, dataUpTo)
	return contracts.ReplayResult{DataUpTo: dataUpTo, Steps: []contracts.StepOutput{}}, nil, f.err
}

func (f *fakeReplayer) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// fakeStarter behaves like Investigator.StartIfNone: only the first call starts
// anything. calls counts how often the run loop asked.
type fakeStarter struct {
	mu      sync.Mutex
	calls   int
	started int
}

func (f *fakeStarter) StartIfNone(context.Context, uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.started == 0 {
		f.started++
	}
	return nil
}

func (f *fakeStarter) counts() (calls, started int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.started
}

type investigationHarness struct {
	*Runner
	store    *fakeRunStore
	analyser *fakeStatisticalAnalyser
	replayer *fakeReplayer
	starter  *fakeStarter
	project  uuid.UUID
}

func newInvestigationHarness() *investigationHarness {
	runner, store, analyser, projectID := newRunnerHarness()
	h := &investigationHarness{Runner: runner, store: store, analyser: analyser, replayer: &fakeReplayer{}, starter: &fakeStarter{}, project: projectID}
	runner.Replayer = h.replayer
	runner.Investigator = h.starter
	return h
}

func (h *investigationHarness) withInvestigation() {
	h.store.invWorkflow = &InvestigationWorkflow{ID: uuid.New(), ProjectID: h.project}
}

func (h *investigationHarness) advance() {
	h.store.mu.Lock()
	h.store.watermark = h.store.watermark.Add(time.Hour)
	h.store.mu.Unlock()
}

func TestInvestigatorNotStartedBeforeFirstStatisticalSuccess(t *testing.T) {
	h := newInvestigationHarness()
	h.analyser.err = errors.New("script failed")
	h.runProject(context.Background(), h.project)
	if calls, _ := h.starter.counts(); calls != 0 {
		t.Fatalf("investigator asked to start %d times before any statistical success", calls)
	}

	// Once the statistical run succeeds, the next pass starts it.
	h.analyser.mu.Lock()
	h.analyser.err = nil
	h.analyser.mu.Unlock()
	h.store.mu.Lock()
	h.store.state = RunState{}
	h.store.mu.Unlock()
	h.runProject(context.Background(), h.project)
	if _, started := h.starter.counts(); started != 1 {
		t.Fatalf("started = %d, want 1", started)
	}
}

func TestInvestigatorStartedOnceAcrossSweepsAndStatisticalRegeneration(t *testing.T) {
	h := newInvestigationHarness()
	ctx := context.Background()
	h.runProject(ctx, h.project)
	h.runProject(ctx, h.project) // a second sweep
	h.advance()
	h.runProject(ctx, h.project) // a new statistical run on new data
	h.store.mu.Lock()
	h.store.workflow = &StatisticalWorkflow{ID: uuid.New(), ProjectID: h.project, Script: "result = {}"} // a regenerated version
	h.store.state = RunState{}
	h.store.mu.Unlock()
	h.runProject(ctx, h.project)
	if _, started := h.starter.counts(); started != 1 {
		t.Fatalf("investigator started %d times, want 1", started)
	}
}

func TestInvestigatorNotAskedToStartWhenOneExists(t *testing.T) {
	h := newInvestigationHarness()
	h.withInvestigation()
	h.runProject(context.Background(), h.project)
	if calls, _ := h.starter.counts(); calls != 0 {
		t.Fatalf("starter asked %d times with an active investigation", calls)
	}
}

func TestSweepWithoutNewDataCreatesNoInvestigationRun(t *testing.T) {
	h := newInvestigationHarness()
	h.withInvestigation()
	ctx := context.Background()
	h.runProject(ctx, h.project)
	h.runProject(ctx, h.project)
	if h.store.invStarts != 1 || h.replayer.count() != 1 {
		t.Fatalf("investigation runs/replays = %d/%d, want 1/1 (the second sweep had no new data)", h.store.invStarts, h.replayer.count())
	}
}

func TestNewDataReplaysWithoutAnyInvestigatorCall(t *testing.T) {
	h := newInvestigationHarness()
	h.withInvestigation()
	ctx := context.Background()
	h.runProject(ctx, h.project)
	h.advance()
	h.runProject(ctx, h.project)
	if h.store.invSuccesses != 2 || h.replayer.count() != 2 {
		t.Fatalf("replays = %d, successes = %d, want 2/2", h.replayer.count(), h.store.invSuccesses)
	}
	if got := h.replayer.at[1]; !got.Equal(h.store.watermark) {
		t.Fatalf("replayed at %v, want the new watermark %v", got, h.store.watermark)
	}
	if calls, _ := h.starter.counts(); calls != 0 {
		t.Fatalf("the investigator (LLM) was asked to start %d times on a data update", calls)
	}
}

func TestFailedStatisticalRunDoesNotBlockReplay(t *testing.T) {
	h := newInvestigationHarness()
	h.withInvestigation()
	h.analyser.err = errors.New("script failed")
	h.runProject(context.Background(), h.project)
	if h.store.failCalls != 1 || h.store.invSuccesses != 1 {
		t.Fatalf("statistical fails = %d, replay successes = %d, want 1/1", h.store.failCalls, h.store.invSuccesses)
	}
}

func TestFailedReplayDoesNotBlockStatisticalRun(t *testing.T) {
	h := newInvestigationHarness()
	h.withInvestigation()
	h.replayer.err = errors.New("every cited step failed")
	h.runProject(context.Background(), h.project)
	if h.store.successCalls != 1 || h.store.invFails != 1 {
		t.Fatalf("statistical successes = %d, replay fails = %d, want 1/1", h.store.successCalls, h.store.invFails)
	}

	// The failed replay backs off like a failed statistical run.
	h.runProject(context.Background(), h.project)
	if h.store.invStarts != 1 {
		t.Fatalf("replay retried during its cooldown (%d runs)", h.store.invStarts)
	}
}

func TestForceRunInvestigationReplaysAtSameWatermark(t *testing.T) {
	h := newInvestigationHarness()
	h.withInvestigation()
	h.SetServiceContext(context.Background())
	watermark := h.store.watermark
	h.store.invState = RunState{SuccessfulDataUpTo: &watermark, LastStatus: "succeeded"}

	if err := h.ForceRun(context.Background(), h.project, KindInvestigation); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for h.replayer.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.replayer.count() != 1 {
		t.Fatalf("replays = %d, want 1", h.replayer.count())
	}
	if h.analyser.callCount() != 0 {
		t.Fatal("forcing the investigation ran the statistical analysis")
	}
}

func TestForceRunInvestigationWithoutWorkflow(t *testing.T) {
	h := newInvestigationHarness()
	h.SetServiceContext(context.Background())
	if err := h.ForceRun(context.Background(), h.project, KindInvestigation); !errors.Is(err, ErrNoActiveInvestigationWorkflow) {
		t.Fatalf("err = %v, want ErrNoActiveInvestigationWorkflow", err)
	}
	if err := h.ForceRun(context.Background(), h.project, "bogus"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
