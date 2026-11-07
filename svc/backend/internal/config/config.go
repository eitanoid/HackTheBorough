// Package config loads service configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr        string
	DatabaseURL string
	StatsURL    string
	LLMStub     bool
	LLMLogPath  string

	// Provider-neutral LLM settings. Model ids are read from the environment
	// and live here and nowhere else.
	LLMAPIKey     string
	ModelProfiler string
	ModelAuthor   string
	ModelNarrator string
	ModelReviewer string

	// ReviewMinInterval is the minimum time between reviews of one finding.
	ReviewMinInterval time.Duration

	IngestTick    time.Duration
	IngestMaxPar  int
	RunDebounce   time.Duration
	RunSweep      time.Duration
	LLMTimeout    time.Duration
	StatsTimeout  time.Duration
	MaxNarrations int
}

func Load() Config {
	return Config{
		Addr:        env("ADDR", ":8080"),
		DatabaseURL: env("DATABASE_URL", ""),
		StatsURL:    env("STATS_URL", "http://stats:8001"),
		LLMStub:     env("LLM_STUB", "") == "1",
		LLMLogPath:  env("LLM_LOG_PATH", "llm_calls.jsonl"),

		LLMAPIKey:     env("LLM_API_KEY", ""),
		ModelProfiler: env("LLM_MODEL_PROFILER", ""),
		ModelAuthor:   env("LLM_MODEL_AUTHOR", ""),
		ModelNarrator: env("LLM_MODEL_NARRATOR", ""),
		ModelReviewer: env("LLM_MODEL_REVIEWER", ""),

		ReviewMinInterval: 60 * time.Second,

		IngestTick:    10 * time.Second,
		IngestMaxPar:  envInt("INGEST_MAX_PAR", 4),
		RunDebounce:   30 * time.Second,
		RunSweep:      5 * time.Minute,
		LLMTimeout:    60 * time.Second,
		StatsTimeout:  60 * time.Second,
		MaxNarrations: 5,
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
