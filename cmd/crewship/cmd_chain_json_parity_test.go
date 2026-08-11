package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chain"
)

// `-f json` must pass the server's answer through, not a subset of it.
//
// This command decodes into its own struct and RE-MARSHALS, so a field the
// struct lacks is silently absent from the output — a JSON consumer sees null
// where the server sent data, and nothing anywhere reports it. chainSummary
// shipped without duration_ms, issues, issue_count, agents and agent_count
// while the server returned all five, and the file already carried a comment
// saying "a field added server-side needs a field here too".
//
// A comment is not a mechanism. This is.
//
// The field set is read from api.ChainSummary — the type the handler marshals —
// and NOT from a captured response, which is how this gate was written first and
// why it stayed green while running_runs and waiting_runs were added server-side.
// A fixture is a photograph of the wire on the day somebody pasted it in; the
// thing it is supposed to catch is precisely the wire changing afterwards. The
// import is test-only, so nothing enters the shipped CLI binary.
//
// Deliberately a shape test rather than a golden response: the failure is always
// "the server grew a field and this struct did not", and the cheapest thing that
// catches it is comparing the two field sets.
func TestChainSummary_CarriesEveryFieldTheServerSends(t *testing.T) {
	server := jsonFieldNames(reflect.TypeOf(api.ChainSummary{}))
	if len(server) == 0 {
		t.Fatal("api.ChainSummary exposed no json fields — the reflection above is reading the wrong type")
	}
	cli := jsonFieldNames(reflect.TypeOf(chainSummary{}))

	var dropped []string
	for name := range server {
		if !cli[name] {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		t.Errorf("chainSummary drops %d field(s) api.ChainSummary sends: %s\n"+
			"`-f json` re-marshals this struct, so these reach a consumer as absent while the "+
			"server returned them. Add the field here when one is added server-side.",
			len(dropped), strings.Join(dropped, ", "))
	}
}

// The SAME gate for the walk, which had none.
//
// `crewship chain <anchor>` decodes into chainNode/chainEdge/chainGap and
// re-marshals them, exactly as the index command does with chainSummary — and
// the struct already carried the same comment ("a field added server-side needs
// a field here too") that the index's version carried while dropping five
// fields. It was a comment there too.
//
// It caught its first field the day it was written: internal/chain.Node grew
// `chain_origin`, the client needed it to tell a chain's own runs from its
// routine's siblings, and `-f json` would have silently omitted it from every
// CLI consumer. One gate over one type is not a mechanism, it is a fix; this is
// the mechanism.
//
// Edges and gaps are covered for completeness rather than because either has
// ever drifted: they are two- and three-field types, which is exactly when
// nobody thinks to check.
func TestChainWalk_CarriesEveryFieldTheServerSends(t *testing.T) {
	for _, c := range []struct {
		what   string
		server reflect.Type
		cli    reflect.Type
	}{
		{"chainNode", reflect.TypeOf(chain.Node{}), reflect.TypeOf(chainNode{})},
		{"chainEdge", reflect.TypeOf(chain.Edge{}), reflect.TypeOf(chainEdge{})},
		{"chainGap", reflect.TypeOf(chain.Gap{}), reflect.TypeOf(chainGap{})},
	} {
		server := jsonFieldNames(c.server)
		if len(server) == 0 {
			t.Fatalf("%s: the server type exposed no json fields — this is reading the wrong type", c.what)
		}
		cli := jsonFieldNames(c.cli)

		var dropped []string
		for name := range server {
			if !cli[name] {
				dropped = append(dropped, name)
			}
		}
		if len(dropped) > 0 {
			sort.Strings(dropped)
			t.Errorf("%s drops %d field(s) the server sends: %s\n"+
				"`crewship chain <anchor> -f json` re-marshals this struct, so these reach a "+
				"consumer as absent while the server returned them.",
				c.what, len(dropped), strings.Join(dropped, ", "))
		}
	}
}

// jsonFieldNames is the set of wire names a struct marshals to. Fields tagged
// "-" and untagged fields are skipped: the first never reaches the wire, and the
// second is a Go-side name this comparison has no opinion about.
func jsonFieldNames(rt reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if name := strings.Split(tag, ",")[0]; name != "" {
			out[name] = true
		}
	}
	return out
}
