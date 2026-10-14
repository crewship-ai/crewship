package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func resetWorkspacePageOwnershipCommand(t *testing.T) {
	t.Helper()
	old := findSubcommand(workspaceCmd.Commands(), "page-ownership")
	fresh := newWorkspacePageOwnershipCommand()
	workspaceCmd.RemoveCommand(old)
	workspaceCmd.AddCommand(fresh)
	t.Cleanup(func() { workspaceCmd.RemoveCommand(fresh); workspaceCmd.AddCommand(old) })
}

func TestWorkspacePageOwnershipCLIRequiresReviewedIdentities(t *testing.T) {
	for _, args := range [][]string{
		{"transfer", "page"}, {"transfer", "page", "--expected-owner", "old"},
		{"transfer", "page", "--successor", "new"},
		{"transfer", "page", "--expected-owner", "old", "--successor", "old"},
		{"transfer", "page", "--expected-owner", " old", "--successor", "new"},
		{"list", "--limit", "0"}, {"list", "--limit", "101"}, {"list", "--cursor", ""},
		{"list", "--owner", ""}, {"list", "--owner", " old"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stub := pageStub(t)
			command := newWorkspacePageOwnershipCommand()
			command.SetArgs(args)
			if err := command.Execute(); err == nil {
				t.Fatalf("accepted incomplete or ambiguous transfer: %v", args)
			}
			if len(stub.Calls()) != 0 {
				t.Fatal("invalid input reached API")
			}
		})
	}
}

func TestWorkspacePageOwnershipCLIListsFilteredMetadataAsJSON(t *testing.T) {
	stub := pageStub(t)
	resetWorkspacePageOwnershipCommand(t)
	endpoint := "/api/v1/workspaces/" + covWSCli3 + "/page-ownership"
	stub.OnGet(endpoint, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("owner_user_id") != "owner+id" || r.URL.Query().Get("workspace_id") != covWSCli3 || r.URL.Query().Get("limit") != "25" {
			t.Errorf("wrong query: %s", r.URL.RawQuery)
		}
		return 200, []byte(`{"pages":[{"id":"page","slug":"report","name":"Private report","type":"artifact","owner_user_id":"owner+id","owner_name":"Original owner","eligible_successors":[{"user_id":"reader","name":"Reader","email":"reader@example.test"}]}]}`), "application/json"
	})
	out, err := runPageCLI(t, "", "workspace", "page-ownership", "list", "--owner", "owner+id", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Pages []struct {
			ID       string `json:"id"`
			Eligible []struct {
				UserID string `json:"user_id"`
			} `json:"eligible_successors"`
		} `json:"pages"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil || len(body.Pages) != 1 || len(body.Pages[0].Eligible) != 1 || body.Pages[0].Eligible[0].UserID != "reader" {
		t.Fatalf("metadata JSON lost eligibility: %s (%v)", out, err)
	}
	if len(stub.CallsFor("GET", endpoint)) != 1 {
		t.Fatal("expected exactly one list request")
	}
}

func TestWorkspacePageOwnershipCLITransfersExactOwnerAndNeverRetriesConflict(t *testing.T) {
	for _, status := range []int{200, 409, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub := pageStub(t)
			resetWorkspacePageOwnershipCommand(t)
			endpoint := "/api/v1/workspaces/" + covWSCli3 + "/page-ownership/page"
			stub.OnPost(endpoint, func(r *http.Request, raw []byte) (int, []byte, string) {
				if r.URL.Query().Get("workspace_id") != covWSCli3 {
					t.Error("workspace scope missing")
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(body, map[string]any{"expected_owner_user_id": "old", "successor_user_id": "reader"}) {
					t.Fatalf("changed transfer authority: %v", body)
				}
				if status != 200 {
					return status, []byte(`{"error":"ownership or permission changed"}`), "application/json"
				}
				return 200, []byte(`{"id":"page","owner_user_id":"reader","eligible_successors":[]}`), "application/json"
			})
			out, err := runPageCLI(t, "", "workspace", "page-ownership", "transfer", "page", "--expected-owner", "old", "--successor", "reader", "--format", "json")
			if (err != nil) != (status != 200) {
				t.Fatalf("status %d output %s error %v", status, out, err)
			}
			if status == 200 && !strings.Contains(out, `"owner_user_id": "reader"`) {
				t.Fatalf("missing transferred summary: %s", out)
			}
			if len(stub.CallsFor("POST", endpoint)) != 1 {
				t.Fatal("transfer must not automatically retry")
			}
			if len(stub.CallsFor("GET", strings.TrimSuffix(endpoint, "/page"))) != 0 {
				t.Fatal("transfer must not silently advance expected owner")
			}
		})
	}
}

func TestWorkspacePageOwnershipCLIKeepsPaginationCursorAndOwnerFilter(t *testing.T) {
	stub := pageStub(t)
	resetWorkspacePageOwnershipCommand(t)
	endpoint := "/api/v1/workspaces/" + covWSCli3 + "/page-ownership"
	stub.OnGet(endpoint, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("cursor") != "opaque+cursor" || r.URL.Query().Get("limit") != "7" || r.URL.Query().Get("owner_user_id") != "old" {
			t.Fatalf("lost pagination scope: %s", r.URL.RawQuery)
		}
		return 200, []byte(`{"pages":[],"next_cursor":"next-token"}`), "application/json"
	})
	out, err := runPageCLI(t, "", "workspace", "page-ownership", "list", "--owner", "old", "--limit", "7", "--cursor", "opaque+cursor", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Next string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Next != "next-token" {
		t.Fatalf("next cursor hidden: %s (%v)", out, err)
	}
	if len(stub.CallsFor("GET", endpoint)) != 1 {
		t.Fatal("list must return the requested page and its cursor without hidden API scans")
	}
}
