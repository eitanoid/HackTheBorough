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

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/api"
	"hacktheburgh/backend/internal/config"
	"hacktheburgh/backend/internal/db"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/llm"
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

	// TODO: start the ingest loop and run loop.

	llmClient, err := llm.New(cfg)
	if err != nil {
		// Keep serving without an LLM so the rest of the app still works.
		slog.Warn("llm disabled", "err", err)
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
	if llmClient != nil {
		analysisStore := &analysis.PgStore{Pool: pool}
		author = &workflows.Author{
			LLM: llmClient,
			Analyser: &analysis.Analyser{
				Store:    analysisStore,
				Runner:   runner,
				Th:       analysis.Thresholds{FDRQ: cfg.FDRQ, MinN: cfg.MinN},
				Timeout:  cfg.ScriptTimeout,
				History:  cfg.AnalysisHistory,
				MaxTests: cfg.MaxTests,
			},
			Store:  &workflows.PgAuthorStore{Pool: pool, Sources: analysisStore},
			Bus:    bus,
			Budget: cfg.AuthorBudget,
		}
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: (&api.Server{Bus: bus, LLM: llmClient, Author: author, DebugLLM: cfg.LLMDebug, AnalysisDebug: cfg.AnalysisDebug, Pool: pool}).Routes()}
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
