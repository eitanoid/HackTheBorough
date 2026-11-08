package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/api"
	"hacktheburgh/backend/internal/charts"
	"hacktheburgh/backend/internal/config"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/db"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/investigate"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/narration"
	"hacktheburgh/backend/internal/pyrunner"
	"hacktheburgh/backend/internal/pyrunner/subproc"
	"hacktheburgh/backend/internal/workflows"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := config.Load()
	bus := events.NewBus()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}

	store := ingest.NewStore(pool)
	adapters := map[string]ingest.SourceAdapter{
		"rss": &ingest.RSSAdapter{},
		"csv": &ingest.CSVAdapter{},
		"web": &ingest.WebAdapter{
			Crawl4AIURL: cfg.Crawl4AIURL,
			Token:       cfg.Crawl4AIToken,
			Provider:    cfg.Crawl4AIProvider,
		},
	}
	loop := ingest.NewLoop(store, bus, adapters, cfg.IngestTick, cfg.IngestMaxPar)
	go loop.Run(ctx)

	llmClient, err := llm.New(cfg)
	if err != nil {
		// Keep serving without an LLM so the rest of the app still works.
		slog.Error("llm disabled", "err", err)
	}

	// The statistical author needs the Python runner. If it can't start, the
	// runner stays nil, generation fails with "analysis unavailable" on the
	// project, and the rest of the app keeps working.
	var runner pyrunner.Runner // stays a nil interface unless the self-check passes
	if err := subproc.Harden(); err != nil {
		slog.Warn("could not mark the backend non-dumpable", "err", err)
	}
	sp := subproc.New(subproc.Config{
		Python:        cfg.PythonBin,
		MaxConcurrent: cfg.RunnerMaxConcurrent,
		MemMB:         cfg.RunnerMemMB,
		MaxResultMB:   cfg.RunnerMaxResultMB,
	})
	checkCtx, cancelCheck := context.WithTimeout(ctx, 45*time.Second)
	versions, err := sp.SelfCheck(checkCtx)
	cancelCheck()
	if err != nil {
		slog.Error("python runner unavailable; analysis is disabled", "err", err)
	} else {
		slog.Info("python runner ready", "python", versions["python"], "pandas", versions["pandas"], "numpy", versions["numpy"])
		runner = sp
	}
	if cfg.AnalysisDebug {
		slog.Warn("ANALYSIS_DEBUG is on: debug endpoints are exposed")
	}
	var author *workflows.Author
	analysisStore := &analysis.PgStore{Pool: pool}
	analyser := &analysis.Analyser{
		Store:    analysisStore,
		Runner:   runner,
		Th:       analysis.Thresholds{FDRQ: cfg.FDRQ, MinN: cfg.MinN},
		Timeout:  cfg.ScriptTimeout,
		History:  cfg.AnalysisHistory,
		MaxTests: cfg.MaxTests,
	}
	insightBuilder := &workflows.InsightBuilder{
		Store: &workflows.PgInsightStore{Pool: pool},
	}
	if llmClient != nil {
		insightBuilder.Narrator = &narration.Narrator{LLM: llmClient}
		insightBuilder.MaxNarrations = cfg.MaxNarrations
		insightBuilder.Chart = charts.Build
		author = &workflows.Author{
			LLM:      llmClient,
			Analyser: analyser,
			Store:    &workflows.PgAuthorStore{Pool: pool, Sources: analysisStore},
			Bus:      bus,
			Budget:   cfg.AuthorBudget,
			Insights: insightBuilder,
		}
	}

	// Investigations replay recorded Python steps, so they need the runner;
	// generating one also needs the LLM. Both stay nil interfaces otherwise.
	var replayer workflows.InvestigationReplayer
	var investigatorStarter workflows.InvestigationStarter
	if runner != nil {
		investigateStore := &investigate.PgStore{Pool: pool, Sources: analysisStore}
		inputs := &investigate.InputBuilder{Store: analysisStore, History: cfg.AnalysisHistory}
		replayer = &investigate.Replayer{
			Store: investigateStore, Inputs: inputs, Runner: runner, Timeout: cfg.RunPythonTimeout, Chart: charts.Build,
		}
		if llmClient != nil {
			investigator := &investigate.Investigator{
				LLM: llmClient, Runner: runner, Store: investigateStore, Inputs: inputs, Bus: bus, Chart: charts.Build,
				Budget: cfg.InvestigatorBudget, MaxSteps: cfg.MaxInvestigatorIt, Timeout: cfg.RunPythonTimeout,
			}
			investigatorStarter = investigator
			// Hooks run in order after the author stores a first run. The
			// reviewer adds its own here.
			hooks := []func(ctx context.Context, projectID uuid.UUID){
				func(ctx context.Context, projectID uuid.UUID) {
					if err := investigator.StartIfNone(ctx, projectID); err != nil {
						slog.Warn("could not start the investigator", "project", projectID, "err", err)
					}
				},
			}
			author.OnFirstRun = func(ctx context.Context, projectID, _, _ uuid.UUID, _ contracts.AnalysisResult) {
				for _, h := range hooks {
					h(ctx, projectID)
				}
			}
		}
	}

	runLoop := &workflows.Runner{
		Store:         &workflows.PgRunStore{Pool: pool, Sources: analysisStore},
		Analyser:      analyser,
		Insights:      insightBuilder,
		Author:        author,
		Replayer:      replayer,
		Investigator:  investigatorStarter,
		Bus:           bus,
		SweepInterval: cfg.RunSweep,
		Debounce:      cfg.RunDebounce,
	}
	runLoop.SetServiceContext(ctx)
	go runLoop.Run(ctx)

	srv := &http.Server{Addr: cfg.Addr, Handler: (&api.Server{
		Bus: bus, LLM: llmClient, Author: author, DebugLLM: cfg.LLMDebug,
		AnalysisDebug: cfg.AnalysisDebug, Pool: pool, Store: store, Ingest: loop, Runner: runLoop,
	}).Routes()}
	go func() {
		slog.Info("backend listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
