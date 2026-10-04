package chain

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestChainStorageOutageCannotMasqueradeAsMissingAnchor(t *testing.T) {
	r := newRig(t, "ws")
	if err := r.db.Close(); err != nil {
		t.Fatal(err)
	}
	w := &walker{db: r.db, workspaceID: r.ws, opt: clampOptions(Options{})}
	for _, lookup := range []struct {
		name string
		fn   func(context.Context, string) (Node, bool, error)
	}{
		{"issue identifier", w.lookupIssueByIdentifier}, {"issue id", w.lookupIssueByID}, {"run", w.lookupRunByID},
		{"routine id", w.lookupRoutineByID}, {"routine slug", w.lookupRoutineBySlug}, {"assignment", w.lookupAssignmentByID},
		{"automation", w.lookupAutomationByID}, {"inbox", w.lookupInboxByID},
	} {
		t.Run(lookup.name, func(t *testing.T) {
			n, found, err := lookup.fn(t.Context(), "anchor")
			if err == nil || errors.Is(err, ErrAnchorNotFound) || found || n.ID != "" {
				t.Fatalf("outage obscured: %+v %v %v", n, found, err)
			}
		})
	}
	if graph, err := Walk(t.Context(), r.db, r.ws, "anchor", Options{}); graph != nil || err == nil || errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("outage returned graph or missing anchor: %+v %v", graph, err)
	}
	for _, kind := range []NodeKind{KindIssue, KindRoutine, KindRun, KindAssignment, KindInbox, KindAutomation} {
		if nodes, err := w.expand(t.Context(), Node{ID: "anchor", Ref: "anchor", Kind: kind}); err == nil || len(nodes) > 0 {
			t.Fatalf("%s outage returned partial graph as success: %+v %v", kind, nodes, err)
		}
	}
	if _, err := w.expand(t.Context(), Node{Kind: "unknown"}); err == nil {
		t.Fatal("unknown node kind silently treated as a leaf")
	}
}

func TestChainDisappearingRowsDoNotInventNeighbours(t *testing.T) {
	r := newRig(t, "ws")
	w := &walker{db: r.db, workspaceID: r.ws, opt: clampOptions(Options{})}
	for _, kind := range []NodeKind{KindRun, KindAssignment, KindInbox, KindAutomation} {
		if nodes, err := w.expand(t.Context(), Node{ID: "removed", Ref: "removed", Kind: kind}); err != nil || len(nodes) != 0 {
			t.Fatalf("%s disappeared row: %+v %v", kind, nodes, err)
		}
	}
	if n, found, err := w.lookupIssueByID(t.Context(), "missing"); err != nil || found || n.ID != "" {
		t.Fatalf("missing issue: %+v %v %v", n, found, err)
	}
	if graph, err := Walk(t.Context(), nil, r.ws, "anchor", Options{}); graph != nil || err == nil {
		t.Fatal("nil store returned a graph")
	}
	if graph, err := Walk(t.Context(), r.db, r.ws, "  ", Options{}); graph != nil || !errors.Is(err, ErrAnchorNotFound) {
		t.Fatal("empty anchor did not fail explicitly")
	}
}

func TestChainMalformedNeighbourRowsAbortCollection(t *testing.T) {
	r := newRig(t, "ws")
	w := &walker{db: r.db, workspaceID: r.ws, opt: clampOptions(Options{})}
	for _, tc := range []struct {
		name, query string
		scan        func(*sql.Rows) (neighbour, error)
	}{
		{"run", `SELECT 'id','slug','completed','bad-depth','','',''`, w.scanRunNeighbour("root", EdgeRuns)},
		{"assignment", `SELECT NULL,'task','completed','',''`, w.scanAssignmentNeighbour("root", EdgeTriggers)},
		{"inbox", `SELECT NULL,'failed_run','title','open',''`, w.scanInboxNeighbour("root")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var nodes []neighbour
			if err := w.collect(t.Context(), &nodes, tc.query, nil, tc.scan); err == nil || len(nodes) > 0 {
				t.Fatalf("malformed row materialized: %+v %v", nodes, err)
			}
		})
	}
}

func TestChainLegacyLabelsRemainIdentifiableAndDanglingEdgesAreDropped(t *testing.T) {
	if n := issueNode("issue", "", "Title", "open"); n.Key != "issue" {
		t.Fatalf("legacy issue has no handle: %+v", n)
	}
	if n := routineNode("routine", "", "slug", "active"); n.Label != "slug" {
		t.Fatalf("unnamed routine has no label: %+v", n)
	}
	if n := agentNode("agent", "", "slug", "active"); n.Label != "slug" || !n.Partial {
		t.Fatalf("legacy agent loses label or gap: %+v", n)
	}
	if n := automationNode("rule", "", "event", true, false); n.Label != "rule" {
		t.Fatalf("unnamed rule has no label: %+v", n)
	}
	if partial, reason := inboxPartial("future-kind"); !partial || reason == "" {
		t.Fatal("unknown inbox kind advertised a complete chain")
	}
	w := &walker{seen: map[string]bool{"root": true}, seenEdge: map[string]bool{}}
	w.addEdge(Edge{From: "root", To: "capped", Kind: EdgeRuns})
	if len(w.edges) != 0 {
		t.Fatal("edge references an absent node")
	}
}
