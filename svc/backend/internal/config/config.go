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
	LLMAPIKey         string
	LLMBaseURL        string // OpenAI-compatible endpoint
	LLMLogPrompts     bool   // include prompt text in the JSONL call log
	LLMDebug          bool   // expose POST /api/debug/chat (off by default)
	ModelProfiler     string
	ModelAuthor       string
	ModelInvestigator string
	ModelReviewer     string
	ModelNarrator     string

	IngestTick    time.Duration
	IngestMaxPar  int
	RunDebounce   time.Duration
	RunSweep      time.Duration
	LLMTimeout    time.Duration
	StatsTimeout  time.Duration
	ToolTimeout   time.Duration
	MaxNarrations int

	// Regeneration limits live in config, not in prompts.
	ReviewMinInterval time.Duration // at most one review per workflow per interval
	MaxRegenerations  int           // per workflow per day
	MaxInvestigatorIt int           // tool-runner iterations
}

func Load() Config {
	return Config{
		Addr:        env("ADDR", ":8080"),
		DatabaseURL: env("DATABASE_URL", ""),
		StatsURL:    env("STATS_URL", "http://stats:8001"),
		LLMStub:     env("LLM_STUB", "") == "1",
		LLMLogPath:  env("LLM_LOG_PATH", "llm_calls.jsonl"),

		LLMAPIKey:         env("OPENAPI_KEY", ""),
		LLMBaseURL:        env("LLM_BASE_URL", ""),
		LLMLogPrompts:     env("LLM_LOG_PROMPTS", "1") == "1",
		LLMDebug:          env("LLM_DEBUG", "") == "1",
		ModelProfiler:     env("LLM_MODEL_PROFILER", ""),
		ModelAuthor:       env("LLM_MODEL_AUTHOR", ""),
		ModelInvestigator: env("LLM_MODEL_INVESTIGATOR", ""),
		ModelReviewer:     env("LLM_MODEL_REVIEWER", ""),
		ModelNarrator:     env("LLM_MODEL_NARRATOR", ""),

		IngestTick:    10 * time.Second,
		IngestMaxPar:  envInt("INGEST_MAX_PAR", 4),
		RunDebounce:   30 * time.Second,
		RunSweep:      5 * time.Minute,
		LLMTimeout:    60 * time.Second,
		StatsTimeout:  60 * time.Second,
		ToolTimeout:   5 * time.Second,
		MaxNarrations: 5,

		ReviewMinInterval: time.Duration(envInt("REVIEW_MIN_INTERVAL", 60)) * time.Second,
		MaxRegenerations:  envInt("MAX_REGENERATIONS", 3),
		MaxInvestigatorIt: 12,
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
