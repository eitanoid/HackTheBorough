// Package workflows holds the statistical author (goal + profiles ->
// WorkflowDraft) and the runner: watermark -> worker /run (statistical) or
// /replay (investigation) -> insights -> hand the run to the reviewer.
// Runs never call the generating agent.
package workflows
