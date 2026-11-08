package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	v1 "hacktheburgh/backend/gen/hacktheburgh/v1"
	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/investigate"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/testdb"
)

// blockingProvider holds every model call until release is closed, so a
// generation stays in flight while the test looks at it.
type blockingProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingProvider) Chat(ctx context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return llm.ChatResponse{}, errors.New("test provider: stopped")
}

func TestConnectRegenerateAndGeneratingState(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	var projectID, sourceID uuid.UUID
	pool.QueryRow(ctx, `INSERT INTO projects (name, goal) VALUES ('Regenerate', 'Test retry') RETURNING id`).Scan(&projectID)
	pool.QueryRow(ctx, `INSERT INTO data_sources (name, kind, config) VALUES ('Regen source', 'csv', '{}'::jsonb) RETURNING id`).Scan(&sourceID)
	pool.Exec(ctx, `INSERT INTO project_sources (project_id, data_source_id) VALUES ($1, $2)`, projectID, sourceID)
	pool.Exec(ctx, `INSERT INTO data_records (data_source_id, external_id, observed_at, payload) VALUES ($1, 'r', now(), '{}'::jsonb)`, sourceID)

	provider := &blockingProvider{entered: make(chan struct{}, 1), release: make(chan struct{})}
	analysisStore := &analysis.PgStore{Pool: pool}
	investigator := &investigate.Investigator{
		LLM:    &llm.Client{Provider: provider, Models: map[llm.Purpose]string{llm.Investigator: "m"}, Timeout: 5 * time.Second, MaxIter: 1},
		Store:  &investigate.PgStore{Pool: pool, Sources: analysisStore},
		Inputs: &investigate.InputBuilder{Store: analysisStore, History: 24 * time.Hour},
	}
	server := httptest.NewServer((&Server{Store: ingest.NewStore(pool), Investigator: investigator}).Routes())
	t.Cleanup(server.Close)
	client := connectTestClient(t, server)

	regenerate := func(kind v1.WorkflowKind) error {
		_, err := client.RegenerateWorkflow(ctx, &v1.RegenerateWorkflowRequest{ProjectId: projectID.String(), Kind: kind})
		return err
	}
	if got := connect.CodeOf(regenerate(v1.WorkflowKind_WORKFLOW_KIND_STATISTICAL)); got != connect.CodeFailedPrecondition {
		t.Fatalf("statistical without an author = %v, want FailedPrecondition", got)
	}
	if got := connect.CodeOf(regenerate(v1.WorkflowKind_WORKFLOW_KIND_UNSPECIFIED)); got != connect.CodeInvalidArgument {
		t.Fatalf("unspecified kind = %v, want InvalidArgument", got)
	}
	if _, err := client.RegenerateWorkflow(ctx, &v1.RegenerateWorkflowRequest{ProjectId: uuid.NewString(), Kind: v1.WorkflowKind_WORKFLOW_KIND_INVESTIGATION}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown project = %v, want NotFound", connect.CodeOf(err))
	}

	generating := func() []v1.WorkflowKind {
		resp, err := client.GetProject(ctx, &v1.GetProjectRequest{Id: projectID.String()})
		if err != nil {
			t.Fatal(err)
		}
		return resp.GetGenerating()
	}
	if g := generating(); len(g) != 0 {
		t.Fatalf("generating before = %v", g)
	}

	if err := regenerate(v1.WorkflowKind_WORKFLOW_KIND_INVESTIGATION); err != nil {
		t.Fatalf("investigation retry: %v", err)
	}
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the investigator never reached the model")
	}
	if g := generating(); len(g) != 1 || g[0] != v1.WorkflowKind_WORKFLOW_KIND_INVESTIGATION {
		t.Fatalf("generating while running = %v", g)
	}
	if got := connect.CodeOf(regenerate(v1.WorkflowKind_WORKFLOW_KIND_INVESTIGATION)); got != connect.CodeFailedPrecondition {
		t.Fatalf("second retry while running = %v, want FailedPrecondition", got)
	}

	close(provider.release)
	deadline := time.Now().Add(5 * time.Second)
	for len(generating()) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if g := generating(); len(g) != 0 {
		t.Fatalf("still generating after the model stopped: %v", g)
	}
	// The failure is what the UI shows next to the retry button.
	var lastError string
	pool.QueryRow(ctx, `SELECT coalesce(last_error, '') FROM projects WHERE id = $1`, projectID).Scan(&lastError)
	if lastError == "" || lastError[:len(investigate.ErrorPrefix)] != investigate.ErrorPrefix {
		t.Fatalf("last_error = %q, want an %q message", lastError, investigate.ErrorPrefix)
	}
}
