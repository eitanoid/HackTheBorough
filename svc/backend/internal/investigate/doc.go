// Package investigate generates and replays an investigation workflow.
//
// The Investigator runs a tool loop over two tools, describe_source and
// run_python, records every call as a workflow step, cleans the model's report
// against the recorded outputs, and saves the version with a first run built
// from those outputs. The Replayer re-executes only the steps that findings
// cite, with no LLM call, whenever new data arrives, and Cards turns the
// outputs into insight rows.
//
// query_series and test_pair are allowed by the workflow_steps CHECK but are
// never offered.
package investigate
