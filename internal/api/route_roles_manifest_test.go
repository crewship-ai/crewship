package api

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateRouteRoles = flag.Bool("update-route-roles", false, "rewrite internal/api/testdata/route-roles.txt from the registered route table")

const routeRolesManifest = "testdata/route-roles.txt"

// TestMutationRouteRolesMatchManifest is the independent half of the route
// authorization contract. Router.mutationRoutes says what the current code
// declares; this checked-in manifest says what the reviewed release contract
// requires. Keeping both statements separate means a role downgrade cannot
// update its own expected value and pass tautologically.
//
// To intentionally change a role, run:
//
//	go test ./internal/api -run TestMutationRouteRolesMatchManifest -update-route-roles
//
// Review the resulting diff and commit it with the router change. A new or
// removed route is also a failure, so the manifest cannot silently drift.
func TestMutationRouteRolesMatchManifest(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	got := make(map[string]string, len(r.mutationRoutes))
	for _, route := range r.mutationRoutes {
		key := route.Method + " " + route.Pattern
		if _, exists := got[key]; exists {
			t.Fatalf("router registers mutation route twice: %s", key)
		}
		got[key] = routeRoleName(route.Role)
	}

	if *updateRouteRoles {
		writeRouteRolesManifest(t, got)
	}
	want := readRouteRolesManifest(t)

	for key, expected := range want {
		actual, ok := got[key]
		if !ok {
			t.Errorf("route role manifest contains stale route %q with %s; remove it or restore the route", key, expected)
			continue
		}
		if actual != expected {
			t.Errorf("%s: manifest requires %s, router declares %s", key, expected, actual)
		}
	}
	for key, actual := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("%s: router declares %s but route is missing from %s", key, actual, routeRolesManifest)
		}
	}
}

func routeRoleName(role string) string {
	switch role {
	case roleCreate:
		return "roleCreate"
	case roleManage:
		return "roleManage"
	case roleSelf:
		return "roleSelf"
	case roleInline:
		return "roleInline"
	default:
		return "<unknown:" + role + ">"
	}
}

func readRouteRolesManifest(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(routeRolesManifest)
	if err != nil {
		t.Fatalf("read %s: %v; run with -update-route-roles to create it", routeRolesManifest, err)
	}
	want := make(map[string]string)
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%s:%d: want METHOD PATH roleName, got %q", routeRolesManifest, lineNo+1, line)
		}
		key := fields[0] + " " + fields[1]
		if _, exists := want[key]; exists {
			t.Fatalf("%s:%d: duplicate route %q", routeRolesManifest, lineNo+1, key)
		}
		want[key] = fields[2]
	}
	return want
}

func writeRouteRolesManifest(t *testing.T, routes map[string]string) {
	t.Helper()
	keys := make([]string, 0, len(routes))
	for key := range routes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Reviewed mutation-route authorization contract.\n")
	b.WriteString("# Regenerate with: go test ./internal/api -run TestMutationRouteRolesMatchManifest -update-route-roles\n")
	for _, key := range keys {
		method, pattern, ok := strings.Cut(key, " ")
		if !ok {
			t.Fatalf("invalid route key %q", key)
		}
		fmt.Fprintf(&b, "%s %s %s\n", method, pattern, routes[key])
	}
	if err := os.MkdirAll(filepath.Dir(routeRolesManifest), 0o755); err != nil {
		t.Fatalf("mkdir manifest directory: %v", err)
	}
	if err := os.WriteFile(routeRolesManifest, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", routeRolesManifest, err)
	}
}
