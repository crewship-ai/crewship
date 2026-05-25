package backup

import (
	"strings"
	"testing"
)

// equalityOrIN is a hot-path helper called by every WorkspaceScopeFilter
// invocation. Pin the boundary cases so a future refactor doesn't
// silently regress index-friendly `= ?` to `IN (?)` for the single-id
// path.
func TestEqualityOrIN_SingleVsMulti(t *testing.T) {
	cases := []struct {
		col  string
		n    int
		want string
	}{
		{`"workspace_id"`, 1, `"workspace_id" = ?`},
		{`"workspace_id"`, 2, `"workspace_id" IN (?, ?)`},
		{`"workspace_id"`, 3, `"workspace_id" IN (?, ?, ?)`},
		{`"id"`, 5, `"id" IN (?, ?, ?, ?, ?)`},
	}
	for _, c := range cases {
		got := equalityOrIN(c.col, c.n)
		if got != c.want {
			t.Errorf("equalityOrIN(%s, %d) = %q, want %q", c.col, c.n, got, c.want)
		}
	}
}

func TestWorkspaceScopeFilter_EmptyJoinPathFailsClosed(t *testing.T) {
	st := ScopedTable{Name: "workspaces"} // empty JoinPath = anchor
	expr, args := st.WorkspaceScopeFilter("ws_1")
	if expr != "1=0" {
		t.Errorf("anchor table filter should be 1=0, got %q", expr)
	}
	if args != nil {
		t.Errorf("anchor filter should have nil args, got %v", args)
	}
}

func TestWorkspaceScopeFilterIDs_EmptyIDListFailsClosed(t *testing.T) {
	st := ScopedTable{
		Name: "chats",
		JoinPath: []ScopeEdge{
			{FromTable: "chats", FromColumn: "workspace_id", ToTable: "workspaces", ToColumn: "id"},
		},
	}
	expr, args := st.WorkspaceScopeFilterIDs(nil)
	if expr != "1=0" {
		t.Errorf("empty ID list should produce 1=0, got %q", expr)
	}
	if args != nil {
		t.Errorf("empty ID list should produce nil args, got %v", args)
	}
}

func TestWorkspaceScopeFilterIDs_DirectDepth1(t *testing.T) {
	st := ScopedTable{
		Name: "chats",
		JoinPath: []ScopeEdge{
			{FromTable: "chats", FromColumn: "workspace_id", ToTable: "workspaces", ToColumn: "id"},
		},
	}
	expr, args := st.WorkspaceScopeFilterIDs([]string{"ws_a", "ws_b"})
	if !strings.Contains(expr, `"workspace_id" IN (?, ?)`) {
		t.Errorf("depth-1 multi-id should produce IN clause, got %q", expr)
	}
	if len(args) != 2 {
		t.Errorf("expected 2 args for 2 ids, got %d", len(args))
	}
}

func TestWorkspaceScopeFilterIDs_TransitiveDepth3(t *testing.T) {
	// agent_skills → agents → crews → workspaces
	st := ScopedTable{
		Name: "agent_skills",
		JoinPath: []ScopeEdge{
			{FromTable: "agent_skills", FromColumn: "agent_id", ToTable: "agents", ToColumn: "id"},
			{FromTable: "agents", FromColumn: "crew_id", ToTable: "crews", ToColumn: "id"},
			{FromTable: "crews", FromColumn: "workspace_id", ToTable: "workspaces", ToColumn: "id"},
		},
	}
	expr, args := st.WorkspaceScopeFilterIDs([]string{"ws_1"})
	// Should nest agent_id IN (SELECT id FROM agents WHERE crew_id IN
	// (SELECT id FROM crews WHERE workspace_id = ?)).
	if !strings.Contains(expr, `"agent_id" IN`) ||
		!strings.Contains(expr, `FROM "agents"`) ||
		!strings.Contains(expr, `FROM "crews"`) ||
		!strings.Contains(expr, `"workspace_id" = ?`) {
		t.Errorf("expected nested subquery chain, got %q", expr)
	}
	if len(args) != 1 {
		t.Errorf("single id should produce one arg, got %d", len(args))
	}
}
