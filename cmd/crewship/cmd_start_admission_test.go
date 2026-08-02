package main

// #1668 — admission control is only real if EVERY provider construction path
// carries the gate. There are four (docker explicit, apple explicit, and both
// again as `auto` candidates), and four literals that each have to remember a
// field is exactly the defect this feature exists to fix, one layer up.
//
// So the four literals were collapsed into two builders, and these tests pin
// that the builders carry the gate along with everything else. A fifth
// construction path that hand-rolls a Config instead of calling the builder
// would show up here as a builder nobody uses.

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/provider"
)

type noopGate struct{}

func (noopGate) Admit(context.Context, string, string, func(string, string)) (func(), error) {
	return func() {}, nil
}

func admissionTestConfig() *config.Config {
	cfg := config.Default()
	cfg.Container.RuntimeImage = "img:test"
	cfg.Container.DefaultRuntime = "runc"
	cfg.Container.Network = "netz"
	cfg.Container.ContainerPrefix = "pfx"
	cfg.Container.SidecarBinaryPath = "/host/sidecar"
	cfg.Container.EntrypointPath = "/host/entrypoint.sh"
	cfg.Storage.BasePath = "/host/base"
	return cfg
}

func TestDockerProviderConfig_CarriesTheAdmissionGateAndEveryField(t *testing.T) {
	gate := noopGate{}
	got := dockerProviderConfig(admissionTestConfig(), gate)

	if got.Admission != provider.AdmissionGate(gate) {
		t.Error("docker provider config does not carry the admission gate")
	}
	for _, tc := range []struct{ field, got, want string }{
		{"RuntimeImage", got.RuntimeImage, "img:test"},
		{"DefaultRuntime", got.DefaultRuntime, "runc"},
		{"Network", got.Network, "netz"},
		{"OutputBasePath", got.OutputBasePath, "/host/base"},
		{"ContainerPrefix", got.ContainerPrefix, "pfx"},
		{"SidecarBinaryPath", got.SidecarBinaryPath, "/host/sidecar"},
		{"EntrypointPath", got.EntrypointPath, "/host/entrypoint.sh"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

func TestAppleProviderConfig_CarriesTheAdmissionGateAndEveryField(t *testing.T) {
	gate := noopGate{}
	got := appleProviderConfig(admissionTestConfig(), gate)

	if got.Admission != provider.AdmissionGate(gate) {
		t.Error("apple provider config does not carry the admission gate")
	}
	for _, tc := range []struct{ field, got, want string }{
		{"RuntimeImage", got.RuntimeImage, "img:test"},
		{"Network", got.Network, "netz"},
		{"OutputBasePath", got.OutputBasePath, "/host/base"},
		{"ContainerPrefix", got.ContainerPrefix, "pfx"},
		{"SidecarBinaryPath", got.SidecarBinaryPath, "/host/sidecar"},
		{"EntrypointPath", got.EntrypointPath, "/host/entrypoint.sh"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

// A nil gate must stay nil rather than becoming a non-nil interface holding a
// nil pointer, which would make the providers' `if gate == nil` short-circuit
// fail and route every start through a controller that does not exist.
func TestProviderConfig_NilGateStaysNil(t *testing.T) {
	if g := dockerProviderConfig(admissionTestConfig(), nil).Admission; g != nil {
		t.Errorf("docker Admission = %v with a nil gate, want nil", g)
	}
	if g := appleProviderConfig(admissionTestConfig(), nil).Admission; g != nil {
		t.Errorf("apple Admission = %v with a nil gate, want nil", g)
	}
}

// The `auto` path must not be a second, gate-less copy of the constructors.
func TestAutoContainerCandidates_UseTheSameGatedBuilders(t *testing.T) {
	cands := autoContainerCandidates(context.Background(), admissionTestConfig(), noopGate{}, covLogger())
	if len(cands) != 2 {
		t.Fatalf("auto candidates = %d, want docker then apple", len(cands))
	}
	if cands[0].name != providerDocker || cands[1].name != providerApple {
		t.Fatalf("candidate order = %q, %q, want docker then apple", cands[0].name, cands[1].name)
	}
	for _, c := range cands {
		if c.admission == nil {
			t.Errorf("auto candidate %q would construct its provider without the admission gate", c.name)
		}
	}
}
