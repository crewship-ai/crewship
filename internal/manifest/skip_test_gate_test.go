package manifest

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// fakeInternalAPIClient records every POST it sees so tests can assert
// the request body shape, and returns a canned 200 response.
type fakeInternalAPIClient struct {
	posts []capturedPost
}

type capturedPost struct {
	path string
	body any
}

func (f *fakeInternalAPIClient) Get(context.Context, string) (*internalapi.Response, error) {
	return &internalapi.Response{StatusCode: 200, Body: strings.NewReader("[]")}, nil
}
func (f *fakeInternalAPIClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.posts = append(f.posts, capturedPost{path: path, body: body})
	return &internalapi.Response{StatusCode: 200, Body: strings.NewReader("{}")}, nil
}
func (f *fakeInternalAPIClient) Patch(_ context.Context, path string, _ any) (*internalapi.Response, error) {
	return &internalapi.Response{StatusCode: 200, Body: strings.NewReader("{}")}, nil
}
func (f *fakeInternalAPIClient) Put(_ context.Context, path string, _ any) (*internalapi.Response, error) {
	return &internalapi.Response{StatusCode: 200, Body: strings.NewReader("{}")}, nil
}
func (f *fakeInternalAPIClient) Delete(context.Context, string) (*internalapi.Response, error) {
	return &internalapi.Response{StatusCode: 204, Body: strings.NewReader("")}, nil
}
func (f *fakeInternalAPIClient) WorkspaceID() string { return "ws_test" }

// TestSkipTestGateDecorator_AddsFieldOnSave verifies that the
// skip-test-gate decorator injects `skip_test_gate: true` into the
// body sent to POST /pipelines/save, leaving every other POST
// untouched. The server gates the field on OWNER/ADMIN role; the
// CLI's job is only to forward it when the operator opts in.
func TestSkipTestGateDecorator_AddsFieldOnSave(t *testing.T) {
	t.Parallel()

	inner := &fakeInternalAPIClient{}
	wrapped := withSkipTestGate(inner)

	// Hit the save path — should get the new field.
	_, err := wrapped.Post(context.Background(),
		"/api/v1/workspaces/ws_test/pipelines/save",
		map[string]any{"slug": "r", "name": "r"},
	)
	if err != nil {
		t.Fatalf("Post save: %v", err)
	}

	// Hit a sibling path — must NOT get the new field.
	_, err = wrapped.Post(context.Background(),
		"/api/v1/workspaces/ws_test/pipeline-schedules",
		map[string]any{"name": "Hourly"},
	)
	if err != nil {
		t.Fatalf("Post schedule: %v", err)
	}

	if len(inner.posts) != 2 {
		t.Fatalf("expected 2 posts; got %d", len(inner.posts))
	}

	savedBody, ok := inner.posts[0].body.(map[string]any)
	if !ok {
		t.Fatalf("save body is %T; want map[string]any", inner.posts[0].body)
	}
	if v, ok := savedBody["skip_test_gate"]; !ok || v != true {
		t.Errorf("save body missing skip_test_gate=true; got %v", savedBody)
	}

	schedBody, ok := inner.posts[1].body.(map[string]any)
	if !ok {
		t.Fatalf("schedule body is %T; want map[string]any", inner.posts[1].body)
	}
	if _, ok := schedBody["skip_test_gate"]; ok {
		t.Errorf("schedule body unexpectedly carries skip_test_gate (decorator scope leaked); got %v", schedBody)
	}
}

// TestSkipTestGateDecorator_NoBodyMutation confirms the decorator
// returns a NEW body map rather than mutating the caller's. Important
// because BuildPlan stashes the body in a closure for re-use across
// dry-run + apply phases.
func TestSkipTestGateDecorator_NoBodyMutation(t *testing.T) {
	t.Parallel()

	original := map[string]any{"slug": "r", "name": "r"}
	wrapped := withSkipTestGate(&fakeInternalAPIClient{})

	_, err := wrapped.Post(context.Background(),
		"/api/v1/workspaces/ws_test/pipelines/save",
		original,
	)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}

	if _, ok := original["skip_test_gate"]; ok {
		t.Errorf("decorator mutated caller's body; original now: %v", original)
	}
}
