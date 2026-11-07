// Package review decides, after each succeeded run, whether a workflow still
// fits the data: hard rules first (no LLM), then an LLM keep/regenerate
// decision from a fixed list of reason categories, capped per day.
// The statistical spec reviewer never sees p-values, findings or the funnel.
package review
