package api

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftassets/craftassetstest"
	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// testCraftProfile is a profile with a real (tiny) WASM engine.
func testCraftProfile(t *testing.T, app string, bundled bool) CraftNativeDocumentProfile {
	t.Helper()
	module := craftassetstest.VersionEngine(app + "-cli 1\n")
	engine, err := craftengine.NewWASM(app, module, "", craftengine.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.Background()) })
	bundle := strings.Repeat("c", 64)
	if app == craftapps.Sheet {
		bundle = strings.Repeat("d", 64)
	}
	return CraftNativeDocumentProfile{Application: app, EngineRevision: strings.Repeat("a", 40), Engine: engine,
		EditorBundle: bundle, EditorURL: "/_editor/b/" + bundle + "/index.html", Bundled: bundled}
}

func workingClock(t *testing.T) func() (mooring.OwnerClock, error) {
	_, native, _ := craftNativeHTTPFixture(t)
	return func() (mooring.OwnerClock, error) { return native.clock, nil }
}

// An operator's explicit native profile still refuses to start without a
// clock, even on a server that can install release packages.
func TestCraftNativeClockFailureWithOperatorProfileStillRefuses(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	install := func(context.Context, string, io.Reader) (CraftNativeDocumentProfile, craftapps.Component, error) {
		return CraftNativeDocumentProfile{}, craftapps.Component{}, errors.New("unused")
	}
	sentinel := errors.New("no boot clock")
	router, err := NewRouter(f.db, "native-agent-task-original-secret-32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{
		Enabled: true, Install: install,
		Profiles:      map[string]CraftNativeDocumentProfile{craftapps.Word: testCraftProfile(t, craftapps.Word, false)},
		newOwnerClock: func() (mooring.OwnerClock, error) { return nil, sentinel }}))
	if router != nil {
		router.Shutdown()
	}
	if !errors.Is(err, sentinel) || router != nil {
		t.Fatalf("operator profile without a clock: router=%v err=%v", router != nil, err)
	}
}

// An installed package that does not qualify at startup leaves only its
// application unavailable; the server and the other application start.
func TestInstalledPackageThatFailsQualificationDoesNotStopTheServer(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	bad := testCraftProfile(t, craftapps.Word, true)
	bad.EditorURL = "https://elsewhere.example/_editor/b/" + bad.EditorBundle + "/index.html"
	good := testCraftProfile(t, craftapps.Sheet, true)
	router, err := NewRouter(f.db, "native-agent-task-original-secret-32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{
		Enabled:       true,
		Profiles:      map[string]CraftNativeDocumentProfile{craftapps.Word: bad, craftapps.Sheet: good},
		Components:    map[string]craftapps.Component{craftapps.Word: {Available: true}, craftapps.Sheet: {Available: true}},
		newOwnerClock: workingClock(t)}))
	if err != nil {
		t.Fatalf("a bad installed package stopped the server: %v", err)
	}
	t.Cleanup(router.Shutdown)
	h := &CraftAppsAdminHandler{r: router}
	if c := h.component(craftapps.Word); c.Available || c.Reason != craftapps.ReasonEngineStartFailed || c.Detail == "" {
		t.Fatalf("Word = %+v", c)
	}
	if _, ok := router.craftNativeDocuments.application(craftapps.Word); ok {
		t.Fatal("unqualified Word registered")
	}
	if c := h.component(craftapps.Sheet); !c.Available {
		t.Fatalf("Sheet = %+v", c)
	}
}

// One installation per application at a time; a second request while it
// runs is refused, and one after it finished finds nothing to install. A
// download that ends with the same package already running is a success.
func TestCraftInstallsDoNotOverlap(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	release := make(chan struct{})
	calls := 0
	profile := testCraftProfile(t, craftapps.Word, true)
	component := craftapps.Component{Available: true, Runtime: craftengine.RuntimeWASM, EngineSHA256: profile.Engine.SHA256(), EditorBundle: profile.EditorBundle}
	install := func(ctx context.Context, app string, _ io.Reader) (CraftNativeDocumentProfile, craftapps.Component, error) {
		calls++
		select {
		case <-release:
		case <-ctx.Done():
			return CraftNativeDocumentProfile{}, craftapps.Component{}, ctx.Err()
		}
		return profile, component, nil
	}
	router, err := NewRouter(f.db, "native-agent-task-original-secret-32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{
		Enabled: true, Install: install,
		Components:    map[string]craftapps.Component{craftapps.Word: {Reason: craftapps.ReasonNotInstalled}},
		newOwnerClock: workingClock(t)}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	h := &CraftAppsAdminHandler{r: router}
	if err := h.startInstall(craftapps.Word); err != nil {
		t.Fatal(err)
	}
	if c := h.component(craftapps.Word); c.Reason != craftapps.ReasonInstalling {
		t.Fatalf("while installing = %+v", c)
	}
	if err := h.startInstall(craftapps.Word); !errors.Is(err, errCraftInstallRunning) && !errors.Is(err, errCraftInstallUnsupported) {
		t.Fatalf("second install while one runs: %v", err)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for !h.component(craftapps.Word).Available {
		if time.Now().After(deadline) {
			t.Fatalf("install did not finish: %+v", h.component(craftapps.Word))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.startInstall(craftapps.Word); !errors.Is(err, errCraftInstallUnsupported) || calls != 1 {
		t.Fatalf("install after it runs: %v (calls %d)", err, calls)
	}
	// The same package arriving again (a request that lost the race) does
	// not turn a running application into a failed one.
	if err := h.finishInstall(context.Background(), craftapps.Word, component, nil); err != nil {
		t.Fatalf("same package again: %v", err)
	}
	if c := h.component(craftapps.Word); !c.Available || c.Reason != "" {
		t.Fatalf("after the same package again = %+v", c)
	}
}
