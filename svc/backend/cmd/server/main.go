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

	"hacktheburgh/backend/internal/api"
	"hacktheburgh/backend/internal/config"
	"hacktheburgh/backend/internal/db"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/llm"
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

	// TODO: start the run loop.

	llmClient, err := llm.New(cfg)
	if err != nil {
		// Keep serving without an LLM so the rest of the app still works.
		slog.Warn("llm disabled", "err", err)
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: (&api.Server{
		Bus: bus, LLM: llmClient, DebugLLM: cfg.LLMDebug, Store: store, Ingest: loop,
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
