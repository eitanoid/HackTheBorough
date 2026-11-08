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
	Crawl4AIURL      string
	Crawl4AIToken    string
	Crawl4AIProvider string
	LLMStub          bool
	LLMLogPath       string

	// Provider-neutral LLM settings. Model ids are read from the environment
	// and live here and nowhere else.
	LLMAPIKey         string
	LLMBaseURL        string // OpenAI-compatible endpoint
	LLMLogPrompts     bool   // include prompt text in the JSONL call log
	LLMDebug          bool   // expose POST /api/debug/chat (off by default)
	AnalysisDebug     bool   // expose /api/debug seed/generate/state
	ModelProfiler     string
	ModelAuthor       string
	ModelInvestigator string
	ModelReviewer     string
	ModelNarrator     string

	// Python runner (restricted child processes of the backend).
	PythonBin           string
	RunnerMaxConcurrent int
	RunnerMemMB         int
	RunnerMaxResultMB   int

	IngestTick       time.Duration
	IngestMaxPar     int
	RunDebounce      time.Duration
	RunSweep         time.Duration
	LLMTimeout       time.Duration
	ScriptTimeout    time.Duration // analysis script, passed to the runner
	RunPythonTimeout time.Duration // investigator run_python tool
	AuthorBudget     time.Duration // overall context for one generation
	FDRQ             float64
	MinN             int
	MaxTests         int
	AnalysisHistory  time.Duration
	MaxNarrations    int

	// Regeneration limits live in config, not in prompts.
	ReviewMinInterval time.Duration // at most one review per workflow per interval
	MaxRegenerations  int           // per workflow per day
	MaxInvestigatorIt int           // tool-runner iterations
}

func Load() Config {
	return Config{
		Addr:             env("ADDR", ":8080"),
		DatabaseURL:      env("DATABASE_URL", ""),
		Crawl4AIURL:      env("CRAWL4AI_API", env("CRAWL4AI_BASE_URL", "http://127.0.0.1:11235")),
		Crawl4AIToken:    env("CRAWL4AI_API_TOKEN", ""),
		Crawl4AIProvider: env("CRAWL4AI_PROVIDER", ""),
		LLMStub:          env("LLM_STUB", "") == "1",
		LLMLogPath:       env("LLM_LOG_PATH", "llm_calls.jsonl"),

		LLMAPIKey:         env("OPENAPI_KEY", ""),
		LLMBaseURL:        env("LLM_BASE_URL", ""),
		LLMLogPrompts:     env("LLM_LOG_PROMPTS", "1") == "1",
		LLMDebug:          env("LLM_DEBUG", "") == "1",
		AnalysisDebug:     env("ANALYSIS_DEBUG", "") == "1",
		ModelProfiler:     env("LLM_MODEL_PROFILER", ""),
		ModelAuthor:       env("LLM_MODEL_AUTHOR", ""),
		ModelInvestigator: env("LLM_MODEL_INVESTIGATOR", ""),
		ModelReviewer:     env("LLM_MODEL_REVIEWER", ""),
		ModelNarrator:     env("LLM_MODEL_NARRATOR", ""),

		PythonBin:           env("PYTHON_BIN", "python3"),
		RunnerMaxConcurrent: envInt("RUNNER_MAX_CONCURRENT", 2),
		RunnerMemMB:         envInt("RUNNER_MEM_MB", 1024),
		RunnerMaxResultMB:   envInt("RUNNER_MAX_RESULT_MB", 16),

		IngestTick:       10 * time.Second,
		IngestMaxPar:     envInt("INGEST_MAX_PAR", 4),
		RunDebounce:      30 * time.Second,
		RunSweep:         5 * time.Minute,
		LLMTimeout:       60 * time.Second,
		ScriptTimeout:    envSeconds("SCRIPT_TIMEOUT_S", 60),
		RunPythonTimeout: envSeconds("RUN_PYTHON_TIMEOUT_S", 30),
		AuthorBudget:     5 * time.Minute,
		FDRQ:             envFloat("FDR_Q", 0.05),
		MinN:             envInt("MIN_N", 30),
		MaxTests:         envInt("MAX_TESTS", 2000),
		AnalysisHistory:  time.Duration(envInt("ANALYSIS_HISTORY_DAYS", 365)) * 24 * time.Hour,
		MaxNarrations:    5,

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

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}

func envSeconds(k string, def int) time.Duration {
	return time.Duration(envInt(k, def)) * time.Second
}
