package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Machine output is the server's document, not this build's reading of it.
//
// A command that decodes a response into a local struct and hands that struct
// to -f json prints only the fields the struct happened to declare. Every field
// the server grows after the struct was written then disappears from the
// machine output, silently and with exit 0: `routine get -f json` lost the
// pending draft and its test result, `skill get` the lifecycle state and
// usage, `workspace get` the workspace kind (#4508). The struct is for the
// human view; the machine formats take the bytes the server sent.

// serverDocumentCap bounds what a single response may hold. Larger than
// cli.ReadJSON's 10 MB because a routine definition plus its manifest is the
// biggest document these commands read, and truncation would be worse than a
// clear error.
const serverDocumentCap = 32 << 20

// readServerDocument reads and closes a successful response's body.
func readServerDocument(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, serverDocumentCap+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(raw) > serverDocumentCap {
		return nil, fmt.Errorf("read response: larger than %d bytes", serverDocumentCap)
	}
	return raw, nil
}

// serverRows splits a JSON array document into one raw document per element,
// so -f ndjson writes a line per row and -f yaml a sequence of mappings. A
// `null` body is an empty list: the machine contract is "[]", never "null".
func serverRows(raw []byte) ([]cli.RawJSON, error) {
	rows := []cli.RawJSON{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return rows, nil
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if rows == nil {
		rows = []cli.RawJSON{}
	}
	return rows, nil
}

// machineMutationResult is the -f json/yaml/ndjson half of a mutation command.
// It reports whether it handled the output: for a human format it writes
// nothing and returns false, leaving the command's own success line alone.
//
// The server's response document is written as it was received. When the
// server answered with no body (204), fallback is written instead, so a
// script that asked for JSON still gets a document saying what happened rather
// than an empty stdout it cannot tell from a failure.
func machineMutationResult(cmd *cobra.Command, raw []byte, fallback any) (bool, error) {
	f := resolvedFormatter(cmd)
	if f.RoutesToHuman() {
		return false, nil
	}
	if len(bytes.TrimSpace(raw)) > 0 && json.Valid(raw) {
		return true, f.Machine(cli.RawJSON(raw))
	}
	return true, f.Machine(fallback)
}

// routineLegacyKeys preserves zero-valued fields emitted by the older CLI
// while retaining every field in the server document, including unknown ones.
func routineLegacyKeys(raw []byte) (cli.RawJSON, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode routine: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("decode routine: expected object")
	}
	for key, value := range map[string]json.RawMessage{
		"description": json.RawMessage(`""`), "last_invocation_status": json.RawMessage(`""`),
		"author_crew_id": json.RawMessage(`""`), "author_agent_id": json.RawMessage(`""`),
		"author_user_id": json.RawMessage(`""`), "last_invoked_at": json.RawMessage(`null`),
	} {
		if _, exists := doc[key]; !exists {
			doc[key] = value
		}
	}
	return json.Marshal(doc)
}
