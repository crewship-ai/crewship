package kinds

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// A crew and an issue in the SAME file could never be applied together.
//
// The Issue kind resolved crew_slug eagerly, at plan time, against the remote
// workspace — but the crew is created by the Workspace document in the very
// same run, and a plan is built before anything executes. So the one shape a
// demo manifest needs most ("here is a crew, and here is an issue in it")
// failed with `crew with slug "demo-ops" not found` before touching the wire.
//
// Routine already got this right: it validates against declared-OR-remote and
// resolves the crew later. Issue now matches, and the ordering it depends on
// is real — BuildPlan walks crews before the standalone kinds.

func TestIssue_Plan_CrewCreatedLaterInTheSameApply(t *testing.T) {
	doc := issueSampleDoc()
	client := newIssueFake()
	// Everything seeded EXCEPT the crew: exactly the state during a first
	// apply of a file that declares both.
	client.projects["network-probes"] = issueProjectStub{ID: "proj_np", Slug: "network-probes"}
	client.agents["viktor"] = issueAgentStub{ID: "agt_viktor", Slug: "viktor"}
	client.labels["network"] = issueLabelStub{ID: "lbl_net", Name: "network"}
	client.labels["monitoring"] = issueLabelStub{ID: "lbl_mon", Name: "monitoring"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan must not fail on a crew this apply will create: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want a single create, got %+v", items)
	}

	// The crew appears — as it would once the workspace document's own items
	// have run — and only then does the issue's Exec resolve it.
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec after the crew exists: %v", err)
	}
	if client.findCall("POST", "/api/v1/crews/crew_eng/issues") == nil {
		t.Error("expected the issue to be POSTed to the crew created meanwhile")
	}
}

func TestIssue_Plan_CrewThatNeverAppearsStillFails(t *testing.T) {
	// Deferring resolution must not become "never check". A crew that is
	// neither remote nor created by this run is a broken manifest, and the
	// failure has to name it rather than 404ing somewhere downstream.
	doc := issueSampleDoc()
	client := newIssueFake()
	client.projects["network-probes"] = issueProjectStub{ID: "proj_np", Slug: "network-probes"}
	client.agents["viktor"] = issueAgentStub{ID: "agt_viktor", Slug: "viktor"}
	client.labels["network"] = issueLabelStub{ID: "lbl_net", Name: "network"}
	client.labels["monitoring"] = issueLabelStub{ID: "lbl_mon", Name: "monitoring"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	err = items[0].Exec(context.Background(), client)
	if err == nil {
		t.Fatal("expected Exec to fail when the crew never materialises")
	}
	if !strings.Contains(err.Error(), "engineering") {
		t.Errorf("the error must name the missing crew, got: %v", err)
	}
}

func TestLookupIssueRemote_MissingCrewMeansNoRemoteIssue(t *testing.T) {
	// A crew that does not exist yet cannot hold an issue, so the honest
	// answer is "no remote row" — which plans a create. Returning an error
	// instead is what made the whole combination unusable.
	client := newIssueFake()

	remote, err := LookupIssueRemoteBySlug(context.Background(), client, "some-slug", "not-created-yet", "Some title")
	if err != nil {
		t.Fatalf("a not-yet-created crew is not an error: %v", err)
	}
	if remote != nil {
		t.Errorf("want nil remote, got %+v", remote)
	}
}
