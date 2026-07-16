package cli

import "strings"

// Crewship mints run ids in two unrelated namespaces, and the commands
// that consume them are split down the same line:
//
//   - Agent (chat-turn) runs — "msg_" (chatbridge generateMsgID) and the
//     legacy "r_". These are journal trace_ids; `crewship history` lists
//     them and `diff` / `inspect` / `explain` read them.
//   - Routine (pipeline) runs — "run_" (pipeline.NewRunID) and the older
//     "prn_" (migration v83). These are rows in `pipeline_runs`;
//     `crewship routine runs <slug>` lists them and the `routine
//     logs`/`report`/`result` family reads them.
//
// The two live behind different endpoints that both 404 with the byte-
// identical message "run not found", so a user who follows the obvious
// path — copy a RUN_ID out of `routine runs`, paste it into `inspect` —
// gets a bare 404 that reads like a missing row rather than a category
// error (#1193). The prefix is the only signal available client-side, so
// the check lives here rather than being inferred from the server reply.
var pipelineRunIDPrefixes = []string{"run_", "prn_"}

// IsPipelineRunID reports whether id belongs to the routine-run namespace.
//
// Note "r_" (legacy agent run) is deliberately not listed: "run_..." does
// not carry the "r_" prefix, so the two namespaces stay unambiguous.
func IsPipelineRunID(id string) bool {
	id = strings.TrimSpace(id)
	for _, p := range pipelineRunIDPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// PipelineRunIDError builds the "you handed me the wrong kind of run id"
// error, naming the command that does accept it.
//
// Exit code stays ExitNotFound (3) — the same code these commands already
// return for an unknown run, via APIError's 404 mapping or fetchRun's own
// NotFoundf. Only the message improves, so scripts branching on the exit
// code are unaffected.
func PipelineRunIDError(id, alternative string) error {
	return NotFoundf(
		"%s is a routine (pipeline) run id; this command reads agent-run ids "+
			"(msg_… from `crewship history`).\nFor routine runs use `%s`.",
		strings.TrimSpace(id), alternative)
}
