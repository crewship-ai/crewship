package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/llm"
)

// ---------------------------------------------------------------------------
// router_options.go — statement coverage for the With* functional options.
//
// Most options are one-line setters returning a RouterOption. Two strategies:
//  1. Apply through NewRouter so both the option body AND the router.go
//     option-application loop execute, then assert the target field landed.
//  2. For options whose value is a concrete dependency that's awkward to
//     build, pass a typed-nil / zero value (the fields are interfaces or
//     pointers, so nil is fine) — the option body still runs.
// ---------------------------------------------------------------------------

const covROSecret = "this-is-a-32-char-test-secret-pad"

func covRONewRouter(t *testing.T, opts ...RouterOption) *Router {
	t.Helper()
	r, err := NewRouter(setupTestDB(t), covROSecret, newTestLogger(), opts...)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if r == nil {
		t.Fatal("NewRouter returned nil router with no error")
	}
	return r
}

// TestCovRO_StringSetters_ApplyThroughNewRouter exercises every string-valued
// option through the real construction path and verifies the field landed.
func TestCovRO_StringSetters_ApplyThroughNewRouter(t *testing.T) {
	r := covRONewRouter(t,
		WithSocketPath("/tmp/cov.sock"),
		WithInternalToken("cov-token"),
		WithInternalBaseURL("http://localhost:9001"),
		WithInternalLoopbackURL("http://127.0.0.1:9002"),
		WithPortExposePublicURL("http://crewship.example.com:8080"),
		WithPortExposeNetwork("crewship-1-agents"),
		WithStoragePath("/tmp/cov-storage"),
		WithFeatureCacheDir("/tmp/cov-features"),
		WithConsolidateMemoryRoot("/tmp/cov-memroot"),
		WithOutputBasePath("/tmp/cov-output"),
		WithMemoryVersionsBlobRoot("/tmp/cov-blobs"),
		WithAllowSignup(true),
		WithGoogleOAuth("client-id", "client-secret", "http://auth.example.com"),
	)

	checks := []struct {
		name, got, want string
	}{
		{"socketPath", r.socketPath, "/tmp/cov.sock"},
		{"internalToken", r.internalToken, "cov-token"},
		{"internalBaseURL", r.internalBaseURL, "http://localhost:9001"},
		{"internalLoopbackURL", r.internalLoopbackURL, "http://127.0.0.1:9002"},
		{"portExposePublicURL", r.portExposePublicURL, "http://crewship.example.com:8080"},
		{"portExposeNetwork", r.portExposeNetwork, "crewship-1-agents"},
		{"storagePath", r.storagePath, "/tmp/cov-storage"},
		{"featureCacheDir", r.featureCacheDir, "/tmp/cov-features"},
		{"consolidateMemoryRoot", r.consolidateMemoryRoot, "/tmp/cov-memroot"},
		{"outputBasePath", r.outputBasePath, "/tmp/cov-output"},
		{"memoryVersionsBlobRoot", r.memoryVersionsBlobRoot, "/tmp/cov-blobs"},
		{"googleClientID", r.googleClientID, "client-id"},
		{"googleSecret", r.googleSecret, "client-secret"},
		{"authBaseURL", r.authBaseURL, "http://auth.example.com"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if !r.allowSignup {
		t.Error("allowSignup = false, want true")
	}
}

// TestCovRO_DependencyOptions_NilTolerant runs the option bodies for the
// interface/pointer-valued options. nil is assignable to every target field,
// so the construction path executes each setter without needing a real
// dependency. WithLogWriter takes a *logcollector.Writer; nil exercises the
// setter body without standing up a real collector.
func TestCovRO_DependencyOptions_NilTolerant(t *testing.T) {
	r := covRONewRouter(t,
		WithHub(nil),
		WithOrchestrator(nil),
		WithKeeperGatekeeper(nil),
		WithKeeperSecrets(nil),
		WithKeeperContainer(nil),
		WithKeeperConfig(nil),
		WithKeeperConversations(nil),
		WithCatalogFetcher(nil),
		WithRuntimeFetcher(nil),
		WithDockerClient(nil),
		WithMissionCallback(nil),
		WithLogWriter(nil),
		WithLicense(nil),
		WithConsolidator(nil),
		WithHybridSearchEmbedder(nil),
		WithHybridSearchProvider(nil),
		WithJournal(nil),
		WithKeeperPhase2Evaluators(nil, nil, nil, nil),
	)
	// Spot-check a couple of fields actually took the nil assignment path
	// (construction completed and didn't crash). The router must still be
	// fully wired.
	if r.authRateLimitedMux == nil || r.apiRateLimitedMux == nil {
		t.Error("rate-limited mux variants must be non-nil after NewRouter")
	}
}

// TestCovRO_AuxiliaryModels_SetsFlag verifies WithAuxiliaryModels marks the
// config as explicitly set so AuxModels() returns the wired (empty) config
// rather than the default fallback.
func TestCovRO_AuxiliaryModels_SetsFlag(t *testing.T) {
	r := covRONewRouter(t, WithAuxiliaryModels(llm.AuxiliaryModels{}))
	if !r.auxModelsSet {
		t.Error("auxModelsSet = false, want true after WithAuxiliaryModels")
	}
}

// TestCovRO_OptionsReturnNonNil calls each With* directly and asserts a
// non-nil RouterOption comes back. This executes the function bodies for the
// options whose concrete dependency we can't easily build, independent of
// NewRouter.
func TestCovRO_OptionsReturnNonNil(t *testing.T) {
	opts := []RouterOption{
		WithSocketPath("x"),
		WithInternalToken("x"),
		WithInternalBaseURL("x"),
		WithInternalLoopbackURL("x"),
		WithPortExposePublicURL("x"),
		WithPortExposeNetwork("x"),
		WithHub(nil),
		WithOrchestrator(nil),
		WithKeeperGatekeeper(nil),
		WithKeeperSecrets(nil),
		WithKeeperContainer(nil),
		WithKeeperConfig(nil),
		WithAllowSignup(false),
		WithGoogleOAuth("a", "b", "c"),
		WithStoragePath("x"),
		WithCatalogFetcher(nil),
		WithRuntimeFetcher(nil),
		WithDockerClient(nil),
		WithFeatureCacheDir("x"),
		WithKeeperConversations(nil),
		WithMissionCallback(nil),
		WithLogWriter(nil),
		WithJournal(nil),
		WithLicense(nil),
		WithConsolidator(nil),
		WithConsolidateMemoryRoot("x"),
		WithOutputBasePath("x"),
		WithMemoryVersionsBlobRoot("x"),
		WithHybridSearchEmbedder(nil),
		WithHybridSearchProvider(nil),
		WithAuxiliaryModels(llm.AuxiliaryModels{}),
		WithKeeperPhase2Evaluators(nil, nil, nil, nil),
	}
	for i, o := range opts {
		if o == nil {
			t.Errorf("option index %d returned a nil RouterOption", i)
		}
	}
}

// TestCovRO_SetterMethods exercises the post-construction *Router setters that
// mirror the options.
func TestCovRO_SetterMethods(t *testing.T) {
	r := covRONewRouter(t)

	r.SetVersion("v1.2.3-cov")
	if r.version != "v1.2.3-cov" {
		t.Errorf("version = %q, want v1.2.3-cov", r.version)
	}

	// AuthHandler is wired during construction.
	if r.AuthHandler() == nil {
		t.Error("AuthHandler() = nil after NewRouter")
	}

	// Legacy post-construction setter for the Phase 2 evaluators.
	r.SetKeeperPhase2Evaluators(nil, nil, nil, nil)
	if r.skillReviewEval != nil || r.behaviorEval != nil || r.memHealthEval != nil || r.negativeEval != nil {
		t.Error("SetKeeperPhase2Evaluators(nil...) should leave evaluators nil")
	}

	// AuxModels falls back to the default set when WithAuxiliaryModels wasn't
	// passed; the call must execute the fallback branch without panicking.
	if r.auxModelsSet {
		t.Error("auxModelsSet = true without WithAuxiliaryModels")
	}
	_ = r.AuxModels()
}
