package api

import (
	"encoding/hex"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// CraftNativeDocumentProfile is trusted release configuration. Operators must
// qualify the exact CLI/source/WASM/bundle together; no HTTP input can set it.
// The validator hashes the opened executable again on every invocation.
// EngineModuleSource loads the verified bytes of an engine module.
type EngineModuleSource struct{ Load func() ([]byte, error) }

type CraftNativeDocumentProfile struct {
	Application    string
	EngineRevision string
	CLIPath        string
	CLISHA256      string
	EditorBundle   string
	// CompatEditors lists, comma separated, earlier editor bundles of the same engine whose
	// documents this editor opens unchanged (a string keeps the profile comparable).
	CompatEditors string
	EditorURL     string
	// Engine runs the document engine. Release profiles carry the bundled WASM
	// engine; when it is nil, CLIPath and CLISHA256 name a native development
	// override (Linux only) and the engine is built from them.
	Engine craftengine.Engine
	// EngineModule provides the verified engine module to a worker process
	// (xlsx import). Nil means the application cannot import. A pointer, so
	// that profiles stay comparable.
	EngineModule *EngineModuleSource
	// Bundled marks a profile built from the components inside this binary.
	Bundled bool
	// Workbook keeps a Sheet as its xlsx or xlsm file (mooring.NativeFormatWorkbook): only with an
	// editor that opens and saves the file itself. Sheet (gridcraft) only.
	Workbook bool
}

func craftNativePin(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// qualifyCraftNativeProfiles checks every profile and returns the store
// configuration plus the same profiles with their engine resolved.
func qualifyCraftNativeProfiles(input map[string]CraftNativeDocumentProfile, trusted func(CraftNativeDocumentProfile, []byte) bool) (map[string]craftNativeApplication, map[string]CraftNativeDocumentProfile, error) {
	profiles := make(map[string]craftNativeApplication, len(input))
	resolved := make(map[string]CraftNativeDocumentProfile, len(input))
	for key, p := range input {
		if key != p.Application || (key != "wordcraft" && key != "gridcraft") || !craftNativePin(p.EngineRevision, 40) || !craftNativePin(p.EditorBundle, 64) {
			return nil, nil, errors.New("invalid native document release profile")
		}
		if p.Engine == nil {
			if !craftNativePin(p.CLISHA256, 64) || !filepath.IsAbs(p.CLIPath) || filepath.Clean(p.CLIPath) != p.CLIPath || strings.ContainsRune(p.CLIPath, 0) {
				return nil, nil, errors.New("invalid native document release profile")
			}
			engine, err := craftengine.NewNative(p.Application, p.CLIPath, p.CLISHA256)
			if err != nil {
				return nil, nil, errors.New("native document CLI unavailable or pin mismatch")
			}
			p.Engine = engine
		} else if p.Engine.Application() != key || !craftNativePin(p.Engine.SHA256(), 64) || (p.CLISHA256 != "" && p.CLISHA256 != p.Engine.SHA256()) {
			return nil, nil, errors.New("invalid native document engine")
		}
		p.CLISHA256 = p.Engine.SHA256()
		u, err := url.Parse(p.EditorURL)
		sameHost := "/_editor/b/" + p.EditorBundle + "/index.html"
		dedicated := "/b/" + p.EditorBundle + "/index.html"
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !((u.Scheme == "" && u.Host == "" && u.Path == sameHost) || (u.Scheme == "https" && u.Host != "" && u.Path == dedicated)) {
			return nil, nil, errors.New("invalid native editor bundle URL")
		}
		if p.Workbook && key != "gridcraft" {
			return nil, nil, errors.New("invalid native document release profile")
		}
		validator := craftNativeValidator(p, trusted)
		store := mooring.NativeStoreConfig{Application: p.Application, EngineRevision: p.EngineRevision, EngineSHA256: p.CLISHA256, EditorBundle: p.EditorBundle, CompatEditorBundles: compatEditorList(p.CompatEditors), Validate: validator.Validate}
		if p.Workbook {
			store.Format, store.Validate = mooring.NativeFormatWorkbook, mooring.ValidateWorkbook
		}
		profiles[key] = craftNativeApplication{Store: store, EditorURL: p.EditorURL}
		resolved[key] = p
	}
	return profiles, resolved, nil
}

// craftNativeValidator validates what is saved with the profile's own engine.
// Bytes this server's own Sheet session wrote with every value computed (its
// snapshot or an import, recorded by digest) are converted without
// recalculating, and only with an engine measured to give the same values;
// anything else, a browser's bytes included, is recalculated as before.
func craftNativeValidator(p CraftNativeDocumentProfile, trusted func(CraftNativeDocumentProfile, []byte) bool) mooring.NativeCLIValidator {
	v := mooring.NativeCLIValidator{Application: p.Application, Engine: p.Engine}
	if p.Application == "gridcraft" && trusted != nil && craftsession.SavedValidationFor(p.CLISHA256) {
		v.Trusted = func(b []byte) bool { return trusted(p, b) }
	}
	return v
}

func compatEditorList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
