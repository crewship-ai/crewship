package manifest

import (
	"context"
	"strings"
	"testing"
)

// TestPlanAutoManagedCredentials_UnchangedOnReapply asserts that
// re-applying a manifest whose auto-managed credential already
// exists in the workspace — same name, same provider=AUTO_MANAGED,
// same provisioned_for_service tag — produces ActionUnchanged in
// the plan output, not ActionCreate.
//
// Pre-fix the plan unconditionally emitted "+ credential" + counted
// "1 created" for these rows, even though the exec closure later
// no-op'd via the existing-row provenance check. The plan output
// is what operators read to know whether a re-apply will mutate
// the workspace; reporting create-on-noop made the dry-run lie and
// turned the post-apply "N created" summary into a permanently
// inflated number.
//
// The fake API preloads a credential with the exact shape the
// SPEC-4 dispatch would have written; BuildPlan must see it and
// flip the predicted action without touching the closure path.
func TestPlanAutoManagedCredentials_UnchangedOnReapply(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - {name: postgres, image: postgres:16-alpine}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t)
	// Preload the auto-managed credential the dispatch would have
	// written on the first apply. The provisioned_for_service tag
	// must match what crewSlug + "/" + ProvisionedForService composes
	// to in planAutoManagedCredentials (t/postgres here).
	provTag := "t/postgres"
	fake.credsByName["POSTGRES_PASSWORD"] = map[string]any{
		"id":                      "cred-1",
		"name":                    "POSTGRES_PASSWORD",
		"provider":                "AUTO_MANAGED",
		"provisioned_for_service": provTag,
	}

	client := NewClient(fake)
	plan, err := BuildPlan(context.Background(), client, b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	var sawAutoManaged bool
	for _, it := range plan.Items {
		if it.Kind != "credential" {
			continue
		}
		if !strings.Contains(it.Description, "POSTGRES_PASSWORD") {
			continue
		}
		if !strings.Contains(it.Description, "auto-managed for "+provTag) {
			continue
		}
		sawAutoManaged = true
		if it.Action != ActionUnchanged {
			t.Errorf("auto-managed credential matching prior state must plan as ActionUnchanged; got %v in item %+v", it.Action, it)
		}
	}
	if !sawAutoManaged {
		t.Fatalf("expected one auto-managed credential plan item for %s; got items: %+v", provTag, plan.Items)
	}
}

// TestPlanAutoManagedCredentials_PropagatesListCredentialsError is
// the CodeRabbit-flagged regression: when the workspace state-read
// fails during planning, the predicted action must NOT silently
// default to ActionCreate. The previous implementation swallowed
// the err and proceeded — which meant a transient server-down or
// network glitch would mislead the plan output for every
// auto-managed credential, then apply would start executing and
// either confusingly succeed (if the next lookup worked) or fail
// mid-stream (if it kept failing). Surfacing the error at plan
// time keeps BuildPlan's "see the whole shape before any mutation
// runs" contract intact.
func TestPlanAutoManagedCredentials_PropagatesListCredentialsError(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - {name: postgres, image: postgres:16-alpine}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b, loadErr := Load(body)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}

	fake := newFakeAPI(t)
	// Inject a 500 on the credentials list path. The Client's
	// fetchBody surfaces non-2xx as an error, which is what every
	// FindCredentialByName caller in BuildPlan must propagate.
	fake.credentialsListStatus = 500

	client := NewClient(fake)
	_, err := BuildPlan(context.Background(), client, b, Options{Mode: ApplyUpsert})
	if err == nil {
		t.Fatal("BuildPlan must surface state-read failures from planning; got nil error")
	}
	// In practice the cross-crew collision check in planCrew fires
	// first and shrouds the err in "crew %q: lookup credential ...",
	// but the planAutoManagedCredentials prediction is the
	// belt-and-suspenders second layer — both wrap the same
	// underlying API error with the credential name. Asserting on
	// the credential name (not on a specific layer's wrapping)
	// keeps the test stable if planCrew's pre-check is later
	// refactored away.
	if !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
		t.Errorf("error should identify the credential whose lookup failed; got %v", err)
	}
}

// TestPlanAutoManagedCredentials_CreateOnFirstApply is the matching
// positive case: an empty workspace must still plan ActionCreate
// for a brand-new auto-managed credential. Pins the no-regression
// half of the unchanged path so a future refactor can't accidentally
// turn every plan into ActionUnchanged.
func TestPlanAutoManagedCredentials_CreateOnFirstApply(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: T, slug: t}
spec:
  services:
    - {name: postgres, image: postgres:16-alpine}
  agents:
    - {slug: a, name: A, agent_role: LEAD, prompt: x}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake := newFakeAPI(t) // empty — no preloaded credentials
	client := NewClient(fake)
	plan, err := BuildPlan(context.Background(), client, b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	for _, it := range plan.Items {
		if it.Kind != "credential" || !strings.Contains(it.Description, "POSTGRES_PASSWORD") {
			continue
		}
		if it.Action != ActionCreate {
			t.Errorf("brand-new auto-managed credential must plan as ActionCreate; got %v in %+v", it.Action, it)
		}
		return
	}
	t.Fatal("expected a POSTGRES_PASSWORD auto-managed credential in the plan")
}
