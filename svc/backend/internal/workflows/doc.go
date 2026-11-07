// Package workflows holds the author (goal + profiles -> WorkflowDraft) and
// the runner: watermark -> stats worker -> diff by finding_key (new / present
// / gone) -> refresh stats and charts for all -> narrate new, review present,
// retire gone -> verify -> store.
package workflows
