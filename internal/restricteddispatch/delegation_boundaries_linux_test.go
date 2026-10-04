//go:build linux

package restricteddispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestDelegatedPreparationRejectsIncompleteAuthorityBeforeBuilder(t *testing.T) {
	a := Authority{}
	command := func(context.Context, string, access.Attempt) ([]string, error) {
		t.Fatal("invalid delegation reached command builder")
		return nil, nil
	}
	prompt := func(context.Context, string, access.Attempt) (NativePrompt, error) {
		t.Fatal("invalid delegation reached prompt builder")
		return NativePrompt{}, nil
	}
	for _, parent := range []string{"", "parent"} {
		var build BoundBuildCommand
		if parent == "" {
			build = command
		}
		if _, _, err := a.PrepareDelegatedResponses(t.Context(), "h1", "w", "a", "c1", parent, nil, 128, build); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("incomplete text delegation accepted: %v", err)
		}
		var nativeBuild BoundBuildNativePrompt
		if parent == "" {
			nativeBuild = prompt
		}
		if _, _, err := a.PrepareDelegatedNative(t.Context(), "h1", "w", "a", "c1", parent, nil, 128, nativeBuild); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("incomplete native delegation accepted: %v", err)
		}
	}
	for _, limit := range []int64{0, 32769} {
		if _, _, err := a.PrepareDelegatedNative(t.Context(), "h1", "w", "a", "c1", "parent", nil, limit, prompt); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("invalid native delegation limit accepted: %v", err)
		}
	}
	if err := a.FreezeWorkflowProvider(t.Context(), nil, "job", "a", "policy", 128); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("policy outside transaction accepted: %v", err)
	}
	if err := a.CheckWorkflowProvider(t.Context(), nil, "job", "a", "policy", 128); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("policy without query accepted: %v", err)
	}
	for _, tc := range []struct {
		attempt access.Attempt
		limit   int64
		profile string
	}{
		{access.Attempt{}, 128, "responses_text"},
		{access.Attempt{Parent: "parent"}, 0, "responses_text"},
		{access.Attempt{Parent: "parent"}, 32769, "native_api_key"},
		{access.Attempt{Parent: "parent"}, 128, "disabled"},
	} {
		if err := a.pinDelegatedProvider(t.Context(), tc.attempt, tc.limit, tc.profile); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("invalid provider binding accepted: %v", err)
		}
	}
	if hasAgentRight([]access.Right{{Kind: "project", ID: "a", Operation: "run"}, {Kind: "agent", ID: "other", Operation: "run"}, {Kind: "agent", ID: "a", Operation: "read"}}, "a", "run") {
		t.Fatal("unrelated right grants agent run")
	}
	if _, _, err := a.prepareBound(t.Context(), "h1", "w", "a", "c1", "", nil, nil, false); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("nil bound builder accepted: %v", err)
	}
	if _, _, err := a.prepareBoundAdmission(t.Context(), "h1", "w", "a", "c1", "", nil, command, true, true); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("chat continuation accepted: %v", err)
	}
	if _, _, err := a.prepareNativeProjectFiles(t.Context(), "h1", "w", "a", "c1", "", nil, 128, nil, nil, false); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("nil project prompt accepted: %v", err)
	}
}

func TestDelegatedRunnerWrappersNeverInventParentProvenance(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "native"}[native], func(t *testing.T) {
			start := func(context.Context, string) (TextSession, error) {
				t.Fatal("unbound delegation reached worker")
				return nil, nil
			}
			a, _ := boundaryRunner(t, native, start)
			text := &TextRunner{Authority: a, MaxOutputTokens: 128, StartSession: start}
			nativeRunner := &NativeRunner{Authority: a, MaxOutputTokens: 128, StartSession: start}
			var absentText *TextRunner
			if absentText.SupportsWorkflowProfile("responses_text") || text.SupportsWorkflowProfile("native_api_key") || !text.SupportsWorkflowProfile("responses_text") {
				t.Fatal("text adapter profile declaration changed")
			}
			run := text.ExecuteDelegatedRun
			if native {
				run = nativeRunner.ExecuteDelegatedRun
			}
			for _, request := range []DelegatedRunRequest{
				{User: "h1", Workspace: "w", Chat: "c1", Agent: "a", Input: "private"},
				{User: "h1", Workspace: "w", Chat: "c1", Agent: "a", Input: "private", ParentHandle: "missing"},
				{User: "h1", Workspace: "w", Chat: "c1", Agent: "a", Input: "private", ParentHandle: "missing", SourceEntryIDs: []string{"missing-source"}},
				{User: "h1", Workspace: "w", Chat: "missing", Agent: "a", Input: "private", ParentHandle: "missing", SourceEntryIDs: []string{"missing-source"}},
			} {
				if err := run(t.Context(), request, func(string, string) error { return nil }); err == nil {
					t.Fatal("unbound delegation succeeded")
				}
			}
			request := DelegatedRunRequest{User: "h1", Workspace: "w", Chat: "c1", Agent: "a", Input: "private", SourceEntryIDs: []string{"orphan"}}
			if _, err := text.ExecuteWorkflowRun(t.Context(), request, func(string, string) error { return nil }); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("orphan proof accepted: %v", err)
			}
		})
	}
}

func TestProviderPlanningDeniesMissingAndDisabledAuthority(t *testing.T) {
	a := providerFixture(t)
	if hash, err := a.ProviderDelegationHash(t.Context(), "missing", "w", "a"); hash != "" || err == nil {
		t.Fatalf("unknown principal selected provider: %q %v", hash, err)
	}
	if hash, err := a.ProviderDelegationHash(t.Context(), "h1", "w", "a"); hash != "" || !errors.Is(err, access.ErrDenied) {
		t.Fatalf("disabled profile selected provider: %q %v", hash, err)
	}
	if err := a.BrokerSettle(t.Context(), "missing", "reservation", restrictedruntime.BrokerUsage{}); err == nil {
		t.Fatal("unknown handle settled cost")
	}
}
