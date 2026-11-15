package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftassets"
	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

// craftApps is the server's view of the Word and Sheet applications (#5315):
// for each one either an operator's development profile (CraftNative with
// Enabled and an explicit native engine) or the package this release trusts,
// downloaded into the store when the administrator turns the application on
// and run in-process as WASM. Whether people may use them is decided per
// request by the instance administrator's setting (craftapps.Policy).
type craftApps struct {
	// operator profiles apply only while CraftNative.Enabled.
	operator   map[string]config.CraftNativeDocumentProfile
	catalog    craftassets.Catalog
	store      craftassets.Store
	components map[string]craftapps.Component
	// installed are the packages found in the store at startup.
	installed map[string]*craftassets.Installed
	locked    map[string]bool
	logger    *slog.Logger

	mu sync.RWMutex
	// pins are the editor bundles of every running application; the native
	// credential gate fences them. Installing an application adds one.
	pins map[string]bool
}

func resolveCraftApps(cfg *config.Config, logger *slog.Logger) *craftApps {
	a := &craftApps{components: map[string]craftapps.Component{}, installed: map[string]*craftassets.Installed{},
		locked: map[string]bool{}, pins: map[string]bool{}, logger: logger}
	n := cfg.CraftNative
	if n.Enabled && len(n.Profiles) > 0 {
		a.operator = n.Profiles
	}
	// Server configuration fixes a setting only when the operator said so
	// explicitly; otherwise the administrator decides in Admin.
	if n.EnabledFromEnv || len(a.operator) > 0 {
		for _, app := range craftapps.Applications {
			a.locked[app] = n.Enabled
		}
	}
	if n.AgentsEnabledFromEnv {
		a.locked[craftapps.Agents] = n.AgentsEnabled
	}
	catalog, err := craftassets.ReleaseCatalog(n.ComponentPins, n.ComponentURL)
	if err != nil {
		logger.Error("Craft component catalog refused", "error", err)
	}
	a.catalog = catalog
	if catalog.Operator {
		logger.Warn("Craft packages are trusted by server configuration, not by this release",
			"pins", craftassets.String(catalog.Pins))
	}
	a.store = craftassets.Store{Root: n.ComponentsDir}
	if a.store.Root == "" && len(catalog.Pins) > 0 {
		if root, err := database.ResolveDataDirRoot(""); err == nil {
			a.store.Root = filepath.Join(root, "craft-components")
		} else {
			logger.Error("no directory for Craft components", "error", err)
		}
	}
	if err := a.store.Sweep(); err != nil {
		logger.Warn("could not remove an interrupted Craft component installation", "error", err)
	}
	for _, app := range craftapps.Applications {
		if p, ok := a.operator[app]; ok {
			a.components[app] = craftapps.Component{Available: true, Development: true, Runtime: craftengine.RuntimeNative,
				EngineRevision: p.EngineRevision, EngineSHA256: p.CLISHA256, EditorBundle: p.EditorBundle}
			a.pins[p.EditorBundle] = true
			continue
		}
		pin, ok := catalog.Pins[app]
		if !ok {
			a.components[app] = craftapps.Component{Reason: craftapps.ReasonComponentMissing}
			continue
		}
		inst, err := a.store.Open(pin)
		switch {
		case errors.Is(err, craftassets.ErrNotInstalled):
			a.components[app] = craftapps.Component{Reason: craftapps.ReasonNotInstalled, Version: pin.Version, Development: catalog.Operator}
		case err != nil:
			logger.Error("installed Craft component refused", "application", app, "error", err)
			a.components[app] = craftapps.Component{Reason: craftapps.ReasonComponentIntegrity, Version: pin.Version, Development: catalog.Operator}
		default:
			a.installed[app] = inst
			a.components[app] = a.componentOf(inst)
		}
	}
	return a
}

// componentOf describes an installed package; it is a development override
// when the operator, not this release, named its pin.
func (a *craftApps) componentOf(inst *craftassets.Installed) craftapps.Component {
	c := inst.Component
	return craftapps.Component{Available: true, Development: a.catalog.Operator, Runtime: craftengine.RuntimeWASM, Version: c.Version,
		EngineRevision: c.EngineRevision, EngineSHA256: c.Engine.SHA256, EditorBundle: c.Editor.Digest}
}

// craftAppsOr returns craft, or the operator-only view of cfg when craft is
// nil (callers and tests that predate the downloadable components).
func craftAppsOr(cfg *config.Config, craft *craftApps) *craftApps {
	if craft != nil {
		return craft
	}
	c := *cfg
	c.CraftNative.ComponentPins, c.CraftNative.ComponentURL = "", ""
	a := resolveCraftApps(&c, slog.New(slog.DiscardHandler))
	a.catalog.Pins = nil
	for app, comp := range a.components {
		if !comp.Development {
			a.components[app] = craftapps.Component{Reason: craftapps.ReasonComponentMissing}
		}
	}
	a.installed = map[string]*craftassets.Installed{}
	return a
}

// enabled reports whether Word or Sheet runs or can be installed.
func (a *craftApps) enabled() bool {
	return a != nil && (len(a.operator) > 0 || len(a.catalog.Pins) > 0)
}

// installable reports whether packages can be installed while running.
func (a *craftApps) installable() bool {
	return a != nil && len(a.catalog.Pins) > 0 && a.store.Root != ""
}

// fenced reports whether digest is the editor bundle of a running application.
func (a *craftApps) fenced(digest string) bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pins[digest]
}

func (a *craftApps) policy() *craftapps.Policy {
	if a == nil {
		return &craftapps.Policy{}
	}
	return &craftapps.Policy{Locked: a.locked}
}

// activate makes an installed package runnable: the editor origin verifies
// and serves its editor, the credential gate fences it, and the engine is
// compiled when it is first used.
func (a *craftApps) activate(editor *editororigin.Handler, inst *craftassets.Installed) (goapi.CraftNativeDocumentProfile, craftapps.Component, error) {
	c := inst.Component
	engine, err := craftengine.NewLazyWASM(c.Application, c.Engine.SHA256, inst.EngineLoader(), craftengine.Limits{})
	if err != nil {
		return goapi.CraftNativeDocumentProfile{}, craftapps.Component{}, err
	}
	if editor == nil {
		return goapi.CraftNativeDocumentProfile{}, craftapps.Component{}, errors.New("the editor origin is disabled")
	}
	// The credential gate fences the pin before the editor can be served.
	a.mu.Lock()
	a.pins[c.Editor.Digest] = true
	a.mu.Unlock()
	if !editor.HasBundle(c.Editor.Digest) {
		if err = editor.AddPackages(inst.EditorPackages()); err != nil {
			return goapi.CraftNativeDocumentProfile{}, craftapps.Component{}, fmt.Errorf("editor refused: %w", err)
		}
	}
	return goapi.CraftNativeDocumentProfile{Application: c.Application, EngineRevision: c.EngineRevision, Engine: engine,
		CLISHA256: c.Engine.SHA256, EditorBundle: c.Editor.Digest, EditorURL: editor.IndexURL(c.Editor.Digest), Bundled: true}, a.componentOf(inst), nil
}

// installer is CraftNativeHTTPConfig.Install: archive nil downloads the
// package this release trusts, otherwise archive must be that package.
func (a *craftApps) installer(editor *editororigin.Handler) goapi.CraftComponentInstall {
	return func(ctx context.Context, app string, archive io.Reader) (goapi.CraftNativeDocumentProfile, craftapps.Component, error) {
		pin, ok := a.catalog.Pins[app]
		if !ok {
			return goapi.CraftNativeDocumentProfile{}, craftapps.Component{}, craftassets.ErrNotPublished
		}
		var inst *craftassets.Installed
		var err error
		if archive == nil {
			inst, err = a.store.Download(ctx, a.catalog, app, nil)
		} else {
			inst, err = a.store.Install(pin, archive)
		}
		if err != nil {
			return goapi.CraftNativeDocumentProfile{}, craftapps.Component{}, err
		}
		return a.activate(editor, inst)
	}
}
