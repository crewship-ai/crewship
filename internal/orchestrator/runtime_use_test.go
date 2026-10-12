package orchestrator

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestRetainRuntimeUseKeepsDetachedOwnershipIndependentOfCaller(t *testing.T) {
	var gate provider.RuntimeUseGate
	release, err := gate.Use(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	parent := provider.NewRuntimeUse("crew", "runtime", release)
	defer parent.Release()
	o := &Orchestrator{}
	child, err := o.retainRuntimeUse(t.Context(), AgentRunRequest{CrewID: "crew", ContainerID: "runtime", RuntimeUse: parent})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Release()
	parent.Release()
	if unlock, ok := gate.TryExclusive(); ok {
		unlock()
		t.Fatal("caller released reservation owned by running invocation")
	}
	child.Release()
	if unlock, ok := gate.TryExclusive(); !ok {
		t.Fatal("completed invocation leaked reservation")
	} else {
		unlock()
	}
}

func TestRetainRuntimeUseRejectsWrongGenerationAndReleasedHandle(t *testing.T) {
	o := &Orchestrator{}
	use := provider.NewRuntimeUse("crew", "generation-one", nil)
	defer use.Release()
	for _, req := range []AgentRunRequest{
		{CrewID: "other", ContainerID: "generation-one", RuntimeUse: use},
		{CrewID: "crew", ContainerID: "generation-two", RuntimeUse: use},
	} {
		if child, err := o.retainRuntimeUse(t.Context(), req); err == nil {
			child.Release()
			t.Fatal("mismatched runtime accepted")
		}
	}
	use.Release()
	if child, err := o.retainRuntimeUse(t.Context(), AgentRunRequest{CrewID: "crew", ContainerID: "generation-one", RuntimeUse: use}); err == nil {
		child.Release()
		t.Fatal("released reservation accepted")
	}
}
