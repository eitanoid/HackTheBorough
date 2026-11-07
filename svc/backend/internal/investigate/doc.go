// Package investigate generates an investigation workflow: a tool-runner
// loop over the worker's read-only tools (describe_source, query_series,
// test_pair) whose calls are recorded as workflow_steps and later replayed
// deterministically, plus the InvestigationReport the agent returns.
package investigate
