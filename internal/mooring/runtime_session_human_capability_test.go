package mooring

import (
	"context"
	"database/sql"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"testing"
)

// Synthetic private state exercises query denials only; it cannot substitute
// for Activate's actual inherited-FD transcript and same-writer SQL proof.
func TestSessionHumanCapabilityRequiresActivatedExactEchoAndLiveProcess(t *testing.T) {
	var empty *SessionActivation
	if empty.RequireHumanNativePrepare() == nil {
		t.Fatal("empty activation acquired human capability")
	}
	versions := RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	makeState := func() (*SessionActivation, *SessionRuntime) {
		s := &SessionRuntime{process: &Runtime{RPC: &RPC{}, done: make(chan struct{}), selectedControlProfile: PrivateSessionProfile, selectedVersions: versions}, versions: versions}
		s.observed.activated = true
		s.observed.requiredCapabilities = []protocol.SessionOpenRequiredCapabilitiesItem{"checkpoint_identity_v1", "human_native_prepare_v1"}
		s.observed.opened = &protocol.SessionOpened{Capabilities: []protocol.SessionOpenedCapabilitiesItem{"checkpoint_identity_v1", "human_native_prepare_v1"}}
		return &SessionActivation{runtime: s, store: &BatchStore{config: BatchStoreConfig{Targets: &TargetStoreConfig{SessionHumanPrepare: func(context.Context, *sql.Tx, SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
			return nil, nil
		}, SessionNativeCandidate: func(context.Context, *sql.Tx, SessionNativeCandidateAccess) error { return nil }}}}}, s
	}
	for _, mode := range []string{"identity-only", "missing-echo", "not-activated", "invalid", "closed", "dead-process", "foreign-profile", "foreign-tuple", "missing-source-resolver", "missing-candidate-validator"} {
		t.Run(mode, func(t *testing.T) {
			a, s := makeState()
			switch mode {
			case "missing-source-resolver":
				a.store.config.Targets.SessionHumanPrepare = nil
			case "missing-candidate-validator":
				a.store.config.Targets.SessionNativeCandidate = nil
			case "identity-only":
				s.observed.requiredCapabilities = []protocol.SessionOpenRequiredCapabilitiesItem{"checkpoint_identity_v1"}
				s.observed.opened.Capabilities = []protocol.SessionOpenedCapabilitiesItem{"checkpoint_identity_v1"}
			case "missing-echo":
				s.observed.opened.Capabilities = []protocol.SessionOpenedCapabilitiesItem{"checkpoint_identity_v1"}
			case "not-activated":
				s.observed.activated = false
			case "invalid":
				s.observed.invalid = true
			case "closed":
				s.closed = true
			case "dead-process":
				close(s.process.done)
			case "foreign-profile":
				s.process.selectedControlProfile = PrivateCausalPrepareProfile
			case "foreign-tuple":
				s.process.selectedVersions.Engine = "99.0.0"
			}
			if a.RequireHumanNativePrepare() == nil {
				t.Fatal("unqualified startup acquired human capability")
			}
		})
	}
	// A matching unit state answers capability provenance only, not permission.
	a, _ := makeState()
	if err := a.RequireHumanNativePrepare(); err != nil {
		t.Fatal("matching query state rejected", err)
	}
}
