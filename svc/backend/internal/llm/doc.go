// Package llm is the only place LLM calls are made: structured outputs with a
// contract struct as the schema, 60s timeout, one retry, a JSONL call log,
// and a stub client when LLM_STUB=1.
package llm
