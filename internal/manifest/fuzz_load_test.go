package manifest

import "testing"

// FuzzLoad fuzzes the manifest YAML/JSON ingestion boundary. Load is the
// single byte-in entrypoint that dispatches raw `crewship apply` documents
// into the internal/manifest/kinds Document structs by sniffing
// apiVersion+kind first — a shared crew template or marketplace manifest is
// exactly the kind of file a user runs without having authored it
// themselves, so a crafted one crashing the CLI is a real (if low-severity)
// concern, not just a hygiene nit.
func FuzzLoad(f *testing.F) {
	seeds := []string{
		"",
		"   \n\t  ",
		`apiVersion: crewship/v1
kind: Agent
metadata:
  name: test-agent
spec:
  llm_provider: ANTHROPIC
`,
		`apiVersion: crewship/v1
kind: Crew
metadata:
  name: test-crew
`,
		`apiVersion: crewship/v1
kind: Project
metadata:
  name: p1
---
apiVersion: crewship/v1
kind: Agent
metadata:
  name: a1
`,
		`apiVersion: wrong/v2
kind: Agent
`,
		`kind: Agent
`,
		`{"apiVersion":"crewship/v1","kind":"Agent","metadata":{"name":"json-form"}}`,
		"apiVersion: crewship/v1\nkind: Agent\nmetadata: [1, 2, 3]\n",
		"apiVersion: [not, a, string]\nkind: Agent\n",
		"- just\n- a\n- list\n",
		"\"just a scalar string\"\n",
		"apiVersion: crewship/v1\nkind: Agent\nspec: &a [*a]\n", // self-referential YAML anchor
		"\x00\x01\x02binary garbage\xff\xfe",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Load(data)
	})
}
