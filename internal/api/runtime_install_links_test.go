package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// installLinks is the "how do I get one" half of GET /api/v1/system/runtime.
// A label with no entry renders as a blank in the console — which is what
// happened to Rancher Desktop: it was in the detector's candidate list from the
// beginning and never in installLinks, and nobody noticed because Detect
// labelled its socket `docker` until #1688 made the name honest.
//
// An assertion that the map has N entries would have passed the day someone
// added the seventh runtime and forgot the link — that is exactly how the hole
// got there. So this walks the detector's OWN candidate list and requires a
// link for every label it can produce. Adding a runtime to
// candidateSocketsFor without an install link fails the build.
func TestInstallLinks_CoverEveryLabelTheDetectorCanProduce(t *testing.T) {
	t.Parallel()

	labels := detectorRuntimeLabels(t)
	if len(labels) < 4 {
		// The scan found almost nothing — the source moved and this test would
		// otherwise pass by vacuously asserting nothing, the failure mode it
		// exists to prevent.
		t.Fatalf("scanned only %d runtime labels from candidateSocketsFor (%v) — "+
			"the parse is stale, not the map", len(labels), labels)
	}

	for _, label := range labels {
		if _, ok := installLinks[label]; !ok {
			t.Errorf("the detector can label a runtime %q but installLinks has no entry for it — "+
				"an operator running it is offered a blank. Add the link in system.go.", label)
		}
	}

	// Apple Containers is not a Docker-API daemon and so is not in that
	// candidate list; the API composes it in from internal/provider/apple, and
	// it needs a link on the same terms.
	if _, ok := installLinks["apple"]; !ok {
		t.Error("installLinks has no entry for apple (composed in from internal/provider/apple)")
	}

	// And nothing may be advertised that the detector cannot produce. containerd
	// is the case in point: it speaks gRPC, never answers the probe (#1687), and
	// an install link for it would send an operator to set up a runtime this
	// server cannot drive.
	known := map[string]bool{"apple": true}
	for _, l := range labels {
		known[l] = true
	}
	for label := range installLinks {
		if !known[label] {
			t.Errorf("installLinks advertises %q, which no detector candidate can ever produce — "+
				"an install link for a runtime Crewship cannot drive", label)
		}
	}
}

// detectorRuntimeLabels parses internal/provider/docker/docker.go and returns
// every runtime label candidateSocketsFor can attach to a socket.
//
// Source-scanned rather than imported because the candidate list is unexported
// — and reading the real file is the point: a copy of the list here would drift
// exactly the way installLinks did.
func detectorRuntimeLabels(t *testing.T) []string {
	t.Helper()

	const src = "../provider/docker/docker.go"
	file, err := parser.ParseFile(token.NewFileSet(), src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "candidateSocketsFor" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatalf("%s: no candidateSocketsFor — the detector's candidate list moved", src)
	}

	seen := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		// unixSock("<path>", "<label>")
		case *ast.CallExpr:
			id, ok := node.Fun.(*ast.Ident)
			if !ok || id.Name != "unixSock" || len(node.Args) != 2 {
				return true
			}
			if lit := stringLit(node.Args[1]); lit != "" {
				seen[lit] = true
			}
		// socketCandidate{path, host, "<label>"} — the Windows named pipe.
		case *ast.CompositeLit:
			if len(node.Elts) != 3 || node.Type != nil {
				return true
			}
			if lit := stringLit(node.Elts[2]); lit != "" {
				seen[lit] = true
			}
		}
		return true
	})

	out := make([]string, 0, len(seen))
	for label := range seen {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func stringLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING || len(lit.Value) < 2 {
		return ""
	}
	return lit.Value[1 : len(lit.Value)-1]
}
