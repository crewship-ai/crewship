package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every way the CLI spells a request path has to come out as a shape, or the
// gate reports a route as unreachable that a command reaches every day. The
// fixture is the union of the shapes found in cmd/crewship on 2026-09-15: a
// literal, a `+` chain through url.PathEscape, an fmt.Sprintf format, a const
// base, a helper whose parameter carries the caller's suffix, a `+=` that
// extends a base, a request built inside a cobra RunE literal, and a client
// method in internal/cli.
func TestCLIPathShapesReadEveryBuilderShape(t *testing.T) {
	sources := []docFile{
		{Path: "cmd/crewship/cmd_a.go", Text: `package main

const roomBase = "/api/v1/conversations"

func roomPath(id string) string { return roomBase + "/" + url.PathEscape(id) }

func workspacePath(client *Client, suffix string) string {
	return "/api/v1/workspaces/" + url.PathEscape(client.WorkspaceID()) + suffix
}

var aCmd = &cobra.Command{
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = client.Get("/api/v1/agents/hire")
		_ = client.Get("/api/v1/agents/" + url.PathEscape(args[0]) + "/avatar")
		_ = client.Get(fmt.Sprintf("/api/v1/admin/users/%s/data?workspace_id=%s", uid, ws))
		_ = client.Get(roomPath(args[0]) + "/messages")
		_ = client.Post(workspacePath(client, "/work-items/"+url.PathEscape(args[0])+"/cancel"), nil)
		endpoint := "/api/v1/pages/" + args[0] + "/project"
		if revision > 0 {
			endpoint += fmt.Sprintf("/history/%d", revision)
		}
		if sourceOnly {
			endpoint += "/source"
		}
		_ = client.Get(endpoint)
		_ = client.Get(fmt.Sprintf("/api/v1/chains%s", qs))
		return nil
	},
}
`},
		{Path: "internal/cli/caps.go", Text: `package cli

func (c *Client) CrewCapabilities(ctx context.Context, crewID string) error {
	_, err := c.Get("/api/v1/crews/" + url.PathEscape(crewID) + "/capabilities")
	return err
}
`},
		// Neither of these is CLI source: a test, and the stub server the
		// tests talk to.
		{Path: "cmd/crewship/cmd_a_test.go", Text: `package main
var _ = "/api/v1/only-in-a-test"
`},
		{Path: "internal/cli/clitest/stub.go", Text: `package clitest
var _ = "/api/v1/only-in-the-stub"
`},
	}

	got, err := cliPathShapes(sources)
	if err != nil {
		t.Fatal(err)
	}
	var shapes []string
	for _, shape := range got {
		shapes = append(shapes, shape.Shape)
	}
	for _, want := range []string{
		"api/v1/agents/hire",
		"api/v1/agents/{}/avatar",
		"api/v1/admin/users/{}/data",
		"api/v1/conversations/{}/messages",
		"api/v1/workspaces/{}/work-items/{}/cancel",
		"api/v1/pages/{}/project",
		"api/v1/pages/{}/project/history/{}",
		"api/v1/pages/{}/project/source",
		"api/v1/pages/{}/project/history/{}/source",
		"api/v1/chains*",
		"api/v1/crews/{}/capabilities",
	} {
		if !slices.Contains(shapes, want) {
			t.Errorf("shape %q not found; got:\n  %s", want, strings.Join(shapes, "\n  "))
		}
	}
	for _, reject := range []string{"api/v1/only-in-a-test", "api/v1/only-in-the-stub"} {
		if slices.Contains(shapes, reject) {
			t.Errorf("shape %q came from a file that is not CLI source", reject)
		}
	}
}

func TestCLIPathShapesRefuseAnEmptyTree(t *testing.T) {
	if _, err := cliPathShapes([]docFile{{Path: "internal/api/router.go", Text: "package api\n"}}); err == nil {
		t.Fatal("no CLI source must be a tooling error, not a report of full parity")
	}
}

func TestShapeMatchesRoute(t *testing.T) {
	routes := map[string]bool{
		"api/v1/workspaces/{}/pipelines/{}":       true,
		"api/v1/workspaces/{}/pipelines/calendar": true,
		"api/v1/workspaces/current":               true,
		"api/v1/workspaces/{}":                    true,
		"api/v1/consolidate/proposed/{}/approve":  true,
		"api/v1/chains":                           true,
		"api/v1/templates":                        true,
	}
	tests := []struct {
		name  string
		shape string
		route string
		want  bool
	}{
		{"literal equals literal", "api/v1/chains", "api/v1/chains", true},
		{"wildcard reaches the parameter route", "api/v1/workspaces/{}/pipelines/{}", "api/v1/workspaces/{}/pipelines/{}", true},
		// The audit's H1: `routine get <slug>` builds /pipelines/{} and the
		// calendar route sat behind it uncounted. A wildcard is not a calendar
		// command when the spec has a parameter route at that position.
		{"wildcard does not claim a literal that has a parameter sibling", "api/v1/workspaces/{}/pipelines/{}", "api/v1/workspaces/{}/pipelines/calendar", false},
		{"wildcard claims a literal with no parameter sibling", "api/v1/consolidate/proposed/{}/{}", "api/v1/consolidate/proposed/{}/approve", true},
		{"shape literal reaches a route parameter", "api/v1/workspaces/current", "api/v1/workspaces/{}", true},
		{"different literals never match", "api/v1/workspaces/current", "api/v1/workspaces/members", false},
		{"segment count must agree", "api/v1/chains/{}", "api/v1/chains", false},
		// `fmt.Sprintf("/api/v1/chains%s", qs)`: the placeholder is a query
		// string or nothing, so the shape reaches chains — and not templates,
		// which a bare wildcard in that position would have claimed.
		{"prefix shape reaches its own resource", "api/v1/chains*", "api/v1/chains", true},
		{"prefix shape does not reach another resource", "api/v1/chains*", "api/v1/templates", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shapeMatchesRoute(tt.shape, tt.route, routes); got != tt.want {
				t.Fatalf("shapeMatchesRoute(%q, %q) = %v, want %v", tt.shape, tt.route, got, tt.want)
			}
		})
	}
}

func TestNormalizeShape(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/api/v1/agents/hire", "api/v1/agents/hire"},
		{"/api/v1/agents/\x00/avatar", "api/v1/agents/{}/avatar"},
		{"/api/v1/agents?crew_id=\x00", "api/v1/agents"},
		{"/api/v1/chains\x00", "api/v1/chains*"},
		{"/api/v1/admin/memory/versions/\x00/content\x00", "api/v1/admin/memory/versions/{}/content*"},
		// A base this tool could not read: dropped rather than matched as a
		// suffix, see normalizeShape.
		{"\x00/cancel", ""},
		{"/healthz", ""},
		{"Backup created: \x00", ""},
	}
	for _, tt := range tests {
		if got := normalizeShape(tt.in); got != tt.want {
			t.Errorf("normalizeShape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCLIParityExemptionsNeedAReason(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	good := write("good.txt", "# comment\n\n/api/v1/pages/runtime/bootstrap   serves the iframe HTML\n/api/v1/webhooks/{token}\tpublic dispatch\n")
	exemptions, err := readCLIParityExemptions(good)
	if err != nil {
		t.Fatal(err)
	}
	if len(exemptions) != 2 || exemptions[0].Path != "/api/v1/pages/runtime/bootstrap" || exemptions[0].Reason != "serves the iframe HTML" || exemptions[1].Line != 4 {
		t.Fatalf("parsed %+v", exemptions)
	}

	bad := write("bad.txt", "/api/v1/pages/runtime/bootstrap\n")
	if _, err := readCLIParityExemptions(bad); err == nil || !strings.Contains(err.Error(), "bad.txt:1") {
		t.Fatalf("an exemption without a reason must fail and name the line, got %v", err)
	}

	if got, err := readCLIParityExemptions(filepath.Join(dir, "absent.txt")); err != nil || got != nil {
		t.Fatalf("a missing file is no exemptions, got %v, %v", got, err)
	}
}

func TestCLIParityForPrefersTheCallerOverTheExemption(t *testing.T) {
	exemptions := []cliParityExemption{{Path: "/api/v1/crews/{crewId}/memory", Reason: "in flight", Line: 3}}
	if got := cliParityFor("/api/v1/crews/{id}/memory", nil, exemptions); got != cliParityExempt {
		t.Errorf("exempt path with no caller = %q, want exempt (parameter names must not matter)", got)
	}
	if got := cliParityFor("/api/v1/crews/{id}/memory", []string{"cmd/crewship/cmd_memory.go"}, exemptions); got != cliParityCovered {
		t.Errorf("exempt path with a caller = %q, want cli", got)
	}
	if got := cliParityFor("/api/v1/orphan", nil, exemptions); got != cliParityMissing {
		t.Errorf("unexempt path with no caller = %q, want missing", got)
	}
}

// An exemption that no longer excuses anything is reported by file and line,
// so the notice is a deletion someone can make — and it is a notice, not a
// failure, so the branch adding the command does not also have to edit the
// exemptions file to go green.
func TestStaleCLIParityExemptionsAreNamed(t *testing.T) {
	exemptions := []cliParityExemption{
		{Path: "/api/v1/crews/{crewId}/memory", Reason: "in flight", Line: 3},
		{Path: "/api/v1/retired", Reason: "gone", Line: 4},
		{Path: "/api/v1/pages/runtime/bootstrap", Reason: "iframe", Line: 5},
	}
	records := []apiRecord{
		{Method: "GET", Path: "/api/v1/crews/{crewId}/memory", CLIParity: cliParityCovered},
		{Method: "GET", Path: "/api/v1/pages/runtime/bootstrap", CLIParity: cliParityExempt},
	}
	got := staleCLIParityExemptions(exemptions, records)
	if len(got) != 2 {
		t.Fatalf("stale = %v, want the covered route and the retired route", got)
	}
	if !strings.Contains(got[0], cliParityExemptionsPath+":3") || !strings.Contains(got[0], "CLI caller") {
		t.Errorf("covered route not reported with its line: %q", got[0])
	}
	if !strings.Contains(got[1], ":4") || !strings.Contains(got[1], "not in the OpenAPI") {
		t.Errorf("retired route not reported with its line: %q", got[1])
	}
}

// The count the gate enforces is per path: a path with GET, PATCH and DELETE
// and no command is one missing command, and the row names the path once.
func TestCLIParityIsCountedPerPath(t *testing.T) {
	r := report{API: []apiRecord{
		{Method: "GET", Path: "/api/v1/orphan", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityMissing},
		{Method: "PATCH", Path: "/api/v1/orphan", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityMissing},
		{Method: "GET", Path: "/api/v1/pages/runtime/bootstrap", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityExempt},
	}}
	r.Summary = summarize(r)
	if r.Summary.APIWithoutCLI != 1 || r.Summary.APIExemptFromCLI != 1 {
		t.Fatalf("summary = without %d, exempt %d; want 1 and 1", r.Summary.APIWithoutCLI, r.Summary.APIExemptFromCLI)
	}
	err := enforce(r)
	if err == nil {
		t.Fatal("a path with no CLI command must fail -strict")
	}
	if strings.Count(err.Error(), "/api/v1/orphan") != 1 {
		t.Errorf("the offending path should be named once:\n%s", err)
	}
	if strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("an exempt path must not be listed as an offender:\n%s", err)
	}
}
