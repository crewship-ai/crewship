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
	if got := cliParityFor("GET", "/api/v1/crews/{id}/memory", nil, exemptions); got != cliParityExempt {
		t.Errorf("exempt path with no caller = %q, want exempt (parameter names must not matter)", got)
	}
	if got := cliParityFor("GET", "/api/v1/crews/{id}/memory", []string{"cmd/crewship/cmd_memory.go"}, exemptions); got != cliParityCovered {
		t.Errorf("exempt path with a caller = %q, want cli", got)
	}
	if got := cliParityFor("GET", "/api/v1/orphan", nil, exemptions); got != cliParityMissing {
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

// Exercise the real command source, not hand-written route declarations: both
// endpoints are already sent by the CLI, behind a callback or format builder.
func TestCLIPathShapesRealBudgetAndTemplateCallers(t *testing.T) {
	for _, tt := range []struct{ name, file, method, route string }{
		{"callback request read", "cmd_crew_budget.go", "GET", "/api/v1/crews/{crewId}/budget"},
		{"callback request set", "cmd_crew_budget.go", "PUT", "/api/v1/crews/{crewId}/budget"},
		{"callback request clear", "cmd_crew_budget.go", "DELETE", "/api/v1/crews/{crewId}/budget"},
		{"formatted suffix", "cmd_routine_templates.go", "POST", "/api/v1/workspaces/{workspaceId}/pipeline-templates/{id}/use"},
		{"peer index", "cmd_feedback.go", "GET", "/api/v1/agents/{agentId}/peers"},
		{"peer entry", "cmd_feedback.go", "DELETE", "/api/v1/agents/{agentId}/peers/{userId}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text, err := os.ReadFile(filepath.Join("..", "..", "cmd", "crewship", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			// postJSON is the existing package-level request wrapper used by
			// the template command. Include its real implementation as well.
			sources := []docFile{{Path: "cmd/crewship/" + tt.file, Text: string(text)}}
			if tt.file == "cmd_routine_templates.go" {
				text, err := os.ReadFile(filepath.Join("..", "..", "cmd", "crewship", "api_helpers.go"))
				if err != nil {
					t.Fatal(err)
				}
				sources = append(sources, docFile{Path: "cmd/crewship/api_helpers.go", Text: string(text)})
			}
			shapes, err := cliPathShapes(sources)
			if err != nil {
				t.Fatal(err)
			}
			routes := map[string]bool{normalizeRoutePath(tt.route): true}
			callers, pathOnly := cliCallers(tt.method, tt.route, shapes, routes)
			if !slices.Contains(callers, "cmd/crewship/"+tt.file) || pathOnly {
				t.Fatalf("actual CLI caller missing for %s %s (path only %v); shapes=%+v", tt.method, tt.route, pathOnly, shapes)
			}
		})
	}
}

func TestCLIPathShapesCallbackSinksStayDeclarationScoped(t *testing.T) {
	source := docFile{Path: "cmd/crewship/cmd_callback.go", Text: `package main
func request(call func(*Client,string) error) { call(client,"/api/v1/actual") }
func describe(call func(*Client,string) error) { call(client,"/api/v1/help-only") }
func invoke() {
 request(func(c *Client,path string) error { return c.Get(path) })
 describe(func(c *Client,path string) error { return log(path) })
}
`}
	shapes, err := cliPathShapes([]docFile{source})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range shapes {
		got = append(got, s.Shape)
	}
	if !slices.Contains(got, "api/v1/actual") {
		t.Errorf("request callback missing: %v", got)
	}
	if slices.Contains(got, "api/v1/help-only") {
		t.Errorf("non-request callback falsely covered: %v", got)
	}
}

// methodsByShape flattens cliPathShapes into shape → the methods its calls
// send, with "?" standing for a call whose method could not be read.
func methodsByShape(t *testing.T, sources []docFile) map[string][]string {
	t.Helper()
	shapes, err := cliPathShapes(sources)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, shape := range shapes {
		methods := append([]string{}, shape.Methods...)
		if shape.MethodUnknown {
			methods = append(methods, "?")
		}
		for _, m := range methods {
			if !slices.Contains(out[shape.Shape], m) {
				out[shape.Shape] = append(out[shape.Shape], m)
			}
		}
	}
	for k := range out {
		slices.Sort(out[k])
	}
	return out
}

// The method of every request call has to be read through the same layers
// the path is: the client method's name, a method argument, a wrapper that
// passes its own method parameter on, a wrapper that dispatches on it, a
// callback, and a request function whose method arrives from its callers.
// Where none of that resolves, the call is method-unknown — never a guess.
func TestCLIPathShapesTrackTheMethod(t *testing.T) {
	tests := []struct {
		name  string
		code  string
		shape string
		want  []string
	}{
		{"client method names", `func f() { client.Patch("/api/v1/a", nil) }`, "api/v1/a", []string{"PATCH"}},
		{"Do with a literal", `func f() { client.Do("put", "/api/v1/a", nil) }`, "api/v1/a", []string{"PUT"}},
		{"Do with net/http constant", `func f() { client.Do(http.MethodDelete, "/api/v1/a", nil) }`, "api/v1/a", []string{"DELETE"}},
		{"NewRequest carries the method second", `func f() { client.NewRequest(ctx, http.MethodPost, "/api/v1/a", nil) }`, "api/v1/a", []string{"POST"}},
		{"local method variable, every binding", `func f() {
	method := "POST"
	if update {
		method = "PATCH"
	}
	client.Do(method, "/api/v1/a", nil)
}`, "api/v1/a", []string{"PATCH", "POST"}},
		{"wrapper passes its method parameter on", `func send(method, path string) error { _, err := client.Do(method, path, nil); return err }
func f() { send("PATCH", "/api/v1/a") }`, "api/v1/a", []string{"PATCH"}},
		{"wrapper of a wrapper", `func send(method, path string) error { _, err := client.Do(method, path, nil); return err }
func sendAgain(method, path string) error { return send(method, path) }
func f() { sendAgain(http.MethodPut, "/api/v1/a") }`, "api/v1/a", []string{"PUT"}},
		{"wrapper with a fixed method", `func fetch(path string) error { _, err := client.Get(path); return err }
func f() { fetch("/api/v1/a") }`, "api/v1/a", []string{"GET"}},
		{"dispatching wrapper sends what the caller names", `func req(method, path string) error {
	if method == "GET" {
		_, err := client.Get(path)
		return err
	}
	_, err := client.Post(path, nil)
	return err
}
func f() { req("GET", "/api/v1/a"); req("POST", "/api/v1/b") }`, "api/v1/a", []string{"GET"}},
		// The dispatching wrapper above sends POST for anything but GET, so a
		// caller naming DELETE does not send DELETE. That is not followed:
		// the call is method-unknown rather than credited with DELETE.
		{"dispatching wrapper given a method it does not send", `func req(method, path string) error {
	if method == http.MethodGet {
		_, err := client.Get(path)
		return err
	}
	_, err := client.Post(path, nil)
	return err
}
func f() { req("DELETE", "/api/v1/a") }`, "api/v1/a", []string{"?"}},
		{"closure wrapper", `func f() {
	send := func(path string) error { _, err := client.Delete(path); return err }
	send("/api/v1/a")
}`, "api/v1/a", []string{"DELETE"}},
		{"callbacks are the union of what is passed", `func run(call func(c *Client, path string) error) { call(client, "/api/v1/a") }
func f() {
	run(func(c *Client, path string) error { return c.Get(path) })
	run(func(c *Client, path string) error { return c.Put(path, nil) })
}`, "api/v1/a", []string{"GET", "PUT"}},
		{"method parameter resolved through the callers", `func milestones(method string, id string) error {
	_, err := client.Do(method, "/api/v1/m/"+id, nil)
	return err
}
func f(verb string) { milestones(map[string]string{"create": "POST", "update": "PUT"}[verb], "x") }
func g() { milestones("DELETE", "y") }`, "api/v1/m/{}", []string{"DELETE", "POST", "PUT"}},
		{"method from a range over literals", `func f() {
	for _, m := range []string{"GET", "DELETE"} {
		client.Do(m, "/api/v1/a", nil)
	}
}`, "api/v1/a", []string{"DELETE", "GET"}},
		{"unreadable method is unknown", `func f() { client.Do(pickMethod(), "/api/v1/a", nil) }`, "api/v1/a", []string{"?"}},
		{"a flag value is unknown", `func f() {
	method, _ := cmd.Flags().GetString("method")
	client.Do(method, "/api/v1/a", nil)
}`, "api/v1/a", []string{"?"}},
		{"a string that is no HTTP method is unknown", `func f() { client.Do("FETCH", "/api/v1/a", nil) }`, "api/v1/a", []string{"?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := methodsByShape(t, []docFile{{Path: "cmd/crewship/cmd_m.go", Text: "package main\n\n" + tt.code + "\n"}})
			if !slices.Equal(got[tt.shape], tt.want) {
				t.Fatalf("methods for %s = %v, want %v (all: %v)", tt.shape, got[tt.shape], tt.want, got)
			}
		})
	}
}

// Two ways the CLI binds a path base that the shape reader used to drop:
// one result of a multi-value call (`client, base, err :=
// guestIssueClient()`), and a function-local constant.
func TestCLIPathShapesBindMultiValueResultsAndLocalConsts(t *testing.T) {
	source := docFile{Path: "cmd/crewship/cmd_b.go", Text: `package main

func guestClient() (*Client, string, error) {
	if err := requireAuth(); err != nil {
		return nil, "", err
	}
	return client, "/api/v1/workspaces/" + url.PathEscape(ws), nil
}

func create() error {
	client, base, err := guestClient()
	if err != nil {
		return err
	}
	_, err = client.Post(base+"/restricted-issues", nil)
	return err
}

func purge() error {
	const path = "/api/v1/admin/keeper/requests"
	_, err := client.Delete(path)
	return err
}

func list() error {
	var path = "/api/v1/admin/keeper/history"
	_, err := client.Get(path)
	return err
}
`}
	got := methodsByShape(t, []docFile{source})
	for shape, want := range map[string][]string{
		"api/v1/workspaces/{}/restricted-issues": {"POST"},
		"api/v1/admin/keeper/requests":           {"DELETE"},
		"api/v1/admin/keeper/history":            {"GET"},
	} {
		if !slices.Equal(got[shape], want) {
			t.Errorf("methods for %s = %v, want %v (all: %v)", shape, got[shape], want, got)
		}
	}
}

// `crewship seed` provisions demo data through the same API, and a route
// only seed calls is not a route a user's command reaches. A declaration in
// cmd_seed*.go counts only when a declaration outside the seed files reaches
// it — which is how the nuke commands' helpers there keep counting.
func TestCLIPathShapesIgnoreSeedOnlyCalls(t *testing.T) {
	sources := []docFile{
		{Path: "cmd/crewship/main.go", Text: `package main
func init() { rootCmd.AddCommand(seedCmd); rootCmd.AddCommand(nukeCmd) }
`},
		{Path: "cmd/crewship/cmd_nuke.go", Text: `package main
var nukeCmd = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error { return nukeThings(client) }}
`},
		// Every file has an init, and the seed command registers its
		// subcommands from one: a name shared by every file is not a call.
		{Path: "cmd/crewship/cmd_seed.go", Text: `package main
var seedCmd = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error { return seedThings(client) }}
func init() { seedCmd.AddCommand(newSeedRosterCmd()) }
func newSeedRosterCmd() *cobra.Command {
	return &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error { _, err := client.Post("/api/v1/seeded-roster", nil); return err }}
}
`},
		{Path: "cmd/crewship/cmd_seed_data_x.go", Text: `package main
func seedThings(c *Client) error { _, err := c.Post("/api/v1/seeded", nil); return (&seeder{}).more() }
func (s *seeder) more() error { _, err := client.Post("/api/v1/seeded-by-method", nil); return err }
func nukeThings(c *Client) error { _, err := c.Delete("/api/v1/nuked"); return err }
`},
	}
	got := methodsByShape(t, sources)
	if !slices.Equal(got["api/v1/nuked"], []string{"DELETE"}) {
		t.Errorf("a seed-file helper the nuke command calls must count: %v", got)
	}
	for _, seedOnly := range []string{"api/v1/seeded", "api/v1/seeded-by-method", "api/v1/seeded-roster"} {
		if _, ok := got[seedOnly]; ok {
			t.Errorf("%s is called only by seed and must not count as CLI coverage: %v", seedOnly, got)
		}
	}
}

func TestCLICoverageIsPerMethod(t *testing.T) {
	shapes := []cliPathShape{
		{Shape: "api/v1/crews/{}", Source: "cmd/crewship/cmd_crew.go", Methods: []string{"GET", "PATCH"}},
		{Shape: "api/v1/crews/{}/budget", Source: "cmd/crewship/cmd_budget.go", MethodUnknown: true, UnknownSites: []string{"cmd/crewship/cmd_budget.go:9"}},
		{Shape: "api/v1/crews/{}/budget", Source: "cmd/crewship/cmd_budget2.go", Methods: []string{"GET"}},
	}
	routes := map[string]bool{"api/v1/crews/{}": true, "api/v1/crews/{}/budget": true}
	tests := []struct {
		method, route string
		wantCallers   []string
		wantPathOnly  bool
	}{
		{"GET", "/api/v1/crews/{crewId}", []string{"cmd/crewship/cmd_crew.go"}, false},
		{"PATCH", "/api/v1/crews/{crewId}", []string{"cmd/crewship/cmd_crew.go"}, false},
		// The CLI reaches the path, but never with PUT: a gap.
		{"PUT", "/api/v1/crews/{crewId}", nil, false},
		// A known-method caller wins; the method-unknown site is not cited.
		{"GET", "/api/v1/crews/{crewId}/budget", []string{"cmd/crewship/cmd_budget2.go"}, false},
		// Only a method-unknown call reaches it: covered by path, and flagged.
		{"DELETE", "/api/v1/crews/{crewId}/budget", []string{"cmd/crewship/cmd_budget.go"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.route, func(t *testing.T) {
			callers, pathOnly := cliCallers(tt.method, tt.route, shapes, routes)
			if !slices.Equal(callers, tt.wantCallers) || pathOnly != tt.wantPathOnly {
				t.Fatalf("cliCallers = %v, pathOnly %v; want %v, %v", callers, pathOnly, tt.wantCallers, tt.wantPathOnly)
			}
		})
	}
}

func TestCLIParityExemptionsTakeAnOptionalMethod(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantMethod string
		wantPath   string
		wantReason string
		wantErr    bool
	}{
		{"path only means every method", "/api/v1/templates   legacy alias", "", "/api/v1/templates", "legacy alias", false},
		{"method prefix", "PUT /api/v1/crews/{crewId}   alias of PATCH", "PUT", "/api/v1/crews/{crewId}", "alias of PATCH", false},
		{"tab separated", "GET\t/api/v1/users/{id}/avatar\timage bytes for <img>", "GET", "/api/v1/users/{id}/avatar", "image bytes for <img>", false},
		{"method with no reason", "PUT /api/v1/crews/{crewId}", "", "", "", true},
		{"method with no path", "PUT   alias of PATCH", "", "", "", true},
		{"lower-case method is not a method", "put /api/v1/crews/{crewId} alias", "", "", "", true},
		{"unknown method", "FETCH /api/v1/crews/{crewId} alias", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ex.txt")
			if err := os.WriteFile(path, []byte(tt.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readCLIParityExemptions(path)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "ex.txt:1") {
					t.Fatalf("want an error naming the line, got %v (%+v)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Method != tt.wantMethod || got[0].Path != tt.wantPath || got[0].Reason != tt.wantReason {
				t.Fatalf("parsed %+v", got)
			}
		})
	}
}

func TestCLIParityForMatchesExemptionMethods(t *testing.T) {
	exemptions := []cliParityExemption{
		{Method: "PUT", Path: "/api/v1/crews/{crewId}", Reason: "alias", Line: 1},
		{Path: "/api/v1/templates", Reason: "legacy", Line: 2},
	}
	tests := []struct {
		method, path string
		covered      bool
		want         string
	}{
		{"PUT", "/api/v1/crews/{id}", false, cliParityExempt},
		{"DELETE", "/api/v1/crews/{id}", false, cliParityMissing},
		{"POST", "/api/v1/templates", false, cliParityExempt},
		{"GET", "/api/v1/templates", false, cliParityExempt},
		{"PUT", "/api/v1/crews/{id}", true, cliParityCovered},
	}
	for _, tt := range tests {
		var callers []string
		if tt.covered {
			callers = []string{"cmd/crewship/cmd_crew.go"}
		}
		if got := cliParityFor(tt.method, tt.path, callers, exemptions); got != tt.want {
			t.Errorf("cliParityFor(%s %s, covered=%v) = %q, want %q", tt.method, tt.path, tt.covered, got, tt.want)
		}
	}
}

// A method-scoped exemption goes stale when that operation gains a caller;
// a path-wide one only when no operation on the path needs it any more.
func TestStaleCLIParityExemptionsArePerMethod(t *testing.T) {
	exemptions := []cliParityExemption{
		{Method: "PUT", Path: "/api/v1/crews/{crewId}", Reason: "alias", Line: 1},
		{Method: "GET", Path: "/api/v1/crews/{crewId}/issues/{identifier}", Reason: "redundant", Line: 2},
		{Path: "/api/v1/templates", Reason: "legacy", Line: 3},
		{Method: "PATCH", Path: "/api/v1/templates", Reason: "no such operation", Line: 4},
	}
	records := []apiRecord{
		{Method: "PUT", Path: "/api/v1/crews/{crewId}", CLIParity: cliParityCovered},
		{Method: "GET", Path: "/api/v1/crews/{crewId}/issues/{identifier}", CLIParity: cliParityExempt},
		{Method: "GET", Path: "/api/v1/templates", CLIParity: cliParityCovered},
		{Method: "POST", Path: "/api/v1/templates", CLIParity: cliParityExempt},
	}
	got := staleCLIParityExemptions(exemptions, records)
	if len(got) != 2 {
		t.Fatalf("stale = %v, want the covered PUT and the absent PATCH", got)
	}
	if !strings.Contains(got[0], ":1") || !strings.Contains(got[0], "PUT /api/v1/crews/{crewId}") || !strings.Contains(got[0], "CLI caller") {
		t.Errorf("covered method exemption not reported: %q", got[0])
	}
	if !strings.Contains(got[1], ":4") || !strings.Contains(got[1], "not in the OpenAPI") {
		t.Errorf("absent operation not reported: %q", got[1])
	}
}

// The gate counts operations: a path the CLI reads but never updates is a
// gap, named with its method. The path-level count stays in the summary for
// the paths no command reaches at all.
func TestCLIParityIsCountedPerOperation(t *testing.T) {
	r := report{API: []apiRecord{
		{Method: "GET", Path: "/api/v1/half", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityCovered},
		{Method: "PATCH", Path: "/api/v1/half", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityMissing},
		{Method: "PUT", Path: "/api/v1/half", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityExempt},
		{Method: "DELETE", Path: "/api/v1/half", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityCovered, CLIMethodUnknown: true},
		{Method: "GET", Path: "/api/v1/orphan", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityMissing},
		{Method: "POST", Path: "/api/v1/orphan", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityMissing},
	}, cliMethodUnknownSites: []string{"cmd/crewship/a.go:1", "cmd/crewship/a.go:9", "cmd/crewship/b.go:4"}}
	r.Summary = summarize(r)
	s := r.Summary
	if s.APIOperationsWithoutCLI != 3 || s.APIOperationsExemptFromCLI != 1 || s.APIOperationsCLIPathOnly != 1 || s.CLIMethodUnknownSites != 3 {
		t.Fatalf("operation counts = %+v", s)
	}
	if s.APIWithoutCLI != 1 || s.APIExemptFromCLI != 0 {
		t.Fatalf("path counts = without %d, exempt %d; want 1 and 0", s.APIWithoutCLI, s.APIExemptFromCLI)
	}
	err := enforce(r)
	if err == nil {
		t.Fatal("an operation with no CLI command must fail -strict")
	}
	for _, row := range []string{"PATCH /api/v1/half", "GET /api/v1/orphan", "POST /api/v1/orphan", "cannot determine", "cmd/crewship/b.go:4"} {
		if !strings.Contains(err.Error(), row) {
			t.Errorf("offender %q not named:\n%s", row, err)
		}
	}
	for _, row := range []string{"PUT /api/v1/half", "DELETE /api/v1/half", "GET /api/v1/half\n"} {
		if strings.Contains(err.Error(), row) {
			t.Errorf("%q is covered or exempt and must not be listed:\n%s", row, err)
		}
	}
	line := cliParitySummaryLine(r)
	for _, part := range []string{"CLI parity 2 API paths, 1 with no command, 0 exempt", "6 operations, 3 with no command for their method, 1 exempt", "1 reached by path only through 3 method-unknown call sites"} {
		if !strings.Contains(line, part) {
			t.Errorf("summary line %q missing %q", line, part)
		}
	}
}

// A name bound in one closure is not visible in its sibling. Two cobra RunE
// literals in one constructor, each with its own `path`, used to have their
// bindings unioned — so the GET claimed the DELETE's path and the reverse.
// That is how GET /pages/{slug}/artifact/versions was falsely covered.
func TestCLIPathShapesScopeBindingsToTheirClosure(t *testing.T) {
	tests := []struct {
		name string
		code string
		want map[string][]string
	}{
		{"sibling closures", `func newXCmd() *cobra.Command {
	list := &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error {
		path := "/api/v1/things"
		_, err := client.Get(path)
		return err
	}}
	del := &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error {
		path := "/api/v1/things/" + args[0]
		_, err := client.Delete(path)
		return err
	}}
	list.AddCommand(del)
	return list
}`, map[string][]string{"api/v1/things": {"GET"}, "api/v1/things/{}": {"DELETE"}}},
		{"an enclosing binding is visible inside", `func newYCmd() *cobra.Command {
	base := "/api/v1/ys"
	return &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error {
		endpoint := base + "/" + args[0]
		_, err := client.Patch(endpoint, nil)
		return err
	}}
}`, map[string][]string{"api/v1/ys/{}": {"PATCH"}}},
		{"the page artifact pattern", `func newPageArtifactCommand() *cobra.Command {
	for _, action := range []string{"create", "publish"} {
		_ = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := "/api/v1/pages/" + url.PathEscape(args[0]) + "/artifact/versions"
			_, err := client.Post(endpoint, nil)
			return err
		}}
	}
	for _, action := range []string{"get", "versions"} {
		_ = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := "/api/v1/pages/" + url.PathEscape(args[0]) + "/artifact"
			_, err := client.Get(endpoint)
			return err
		}}
	}
	return nil
}`, map[string][]string{"api/v1/pages/{}/artifact/versions": {"POST"}, "api/v1/pages/{}/artifact": {"GET"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := methodsByShape(t, []docFile{{Path: "cmd/crewship/cmd_s.go", Text: "package main\n\n" + tt.code + "\n"}})
			if len(got) != len(tt.want) {
				t.Fatalf("shapes = %v, want %v", got, tt.want)
			}
			for shape, want := range tt.want {
				if !slices.Equal(got[shape], want) {
					t.Errorf("methods for %s = %v, want %v (all: %v)", shape, got[shape], want, got)
				}
			}
		})
	}
}

// Seed code must not lend a user-facing request function its methods —
// neither as the method argument it is called with, nor as the callback it
// is passed — and a method that only shares a name with a seed type's
// method does not make that seed method reachable.
func TestCLIPathShapesSeedCallersLendNoMethods(t *testing.T) {
	sources := []docFile{
		{Path: "cmd/crewship/cmd_y.go", Text: `package main
func yRequest(method, id string) error { _, err := client.Do(method, "/api/v1/y/"+id, nil); return err }
func runZ(call func(c *Client, path string) error) error { return call(client, "/api/v1/z") }
var yCmd = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error {
	_ = runZ(func(c *Client, path string) error { _, err := c.Get(path); return err })
	return yRequest("GET", args[0])
}}
var doctorCmd = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error { return check.run() }}
var verifyCmd = &cobra.Command{RunE: func(cmd *cobra.Command, args []string) error { return newVerifier().verify() }}
`},
		{Path: "cmd/crewship/cmd_seed_y.go", Text: `package main
func seedY() error {
	_ = runZ(func(c *Client, path string) error { _, err := c.Delete(path); return err })
	return yRequest("DELETE", "z")
}
type packVerifier struct{}
func (v *packVerifier) run() error { _, err := client.Get("/api/v1/seed-verify"); return err }
type verifier struct{}
func newVerifier() *verifier { return &verifier{} }
func (v *verifier) verify() error { _, err := client.Get("/api/v1/user-verify"); return err }
`},
	}
	got := methodsByShape(t, sources)
	for shape, want := range map[string][]string{
		"api/v1/y/{}":        {"GET"},
		"api/v1/z":           {"GET"},
		"api/v1/user-verify": {"GET"},
	} {
		if !slices.Equal(got[shape], want) {
			t.Errorf("methods for %s = %v, want %v (all: %v)", shape, got[shape], want, got)
		}
	}
	if _, ok := got["api/v1/seed-verify"]; ok {
		t.Errorf("check.run() must not reach (*packVerifier).run: %v", got)
	}
}

// Method wrappers are matched by name at the call site, where the receiver's
// type is unknown. Two same-named wrappers sending different methods must
// not lend each other theirs: the call is method-unknown instead.
func TestCLIPathShapesMethodWrappersOfOneNameDoNotShareMethods(t *testing.T) {
	got := methodsByShape(t, []docFile{{Path: "cmd/crewship/cmd_r.go", Text: `package main
type reader struct{}
type deleter struct{}
func (r *reader) send(path string) error { _, err := client.Get(path); return err }
func (d *deleter) send(path string) error { _, err := client.Delete(path); return err }
func f() { r.send("/api/v1/s") }
`}})
	if !slices.Contains(got["api/v1/s"], "?") {
		t.Fatalf("methods for api/v1/s = %v, want the call to be method-unknown", got["api/v1/s"])
	}
}

// The method-unknown fallback has a ratchet: -strict fails on any such call
// site even when every operation is otherwise covered.
func TestStrictFailsOnMethodUnknownCallSites(t *testing.T) {
	r := report{API: []apiRecord{
		{Method: "GET", Path: "/api/v1/a", Status: "documented_exact", ConcreteResponseSchema: true, CLIParity: cliParityCovered, CLIMethodUnknown: true},
	}, cliMethodUnknownSites: []string{"cmd/crewship/cmd_a.go:12"}}
	r.Summary = summarize(r)
	err := enforce(r)
	if err == nil || !strings.Contains(err.Error(), "cmd/crewship/cmd_a.go:12") {
		t.Fatalf("a method-unknown call site must fail -strict and be named, got %v", err)
	}
}
