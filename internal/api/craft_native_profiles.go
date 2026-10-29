package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// CraftNativeDocumentProfile is trusted release configuration. Operators must
// qualify the exact CLI/source/WASM/bundle together; no HTTP input can set it.
// The validator hashes the opened executable again on every invocation.
type CraftNativeDocumentProfile struct {
	Application    string
	EngineRevision string
	CLIPath        string
	CLISHA256      string
	EditorBundle   string
	EditorURL      string
}

func craftNativePin(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func qualifyCraftNativeProfiles(input map[string]CraftNativeDocumentProfile) (map[string]craftNativeApplication, error) {
	profiles := make(map[string]craftNativeApplication, len(input))
	for key, p := range input {
		if key != p.Application || (key != "wordcraft" && key != "gridcraft") || !craftNativePin(p.EngineRevision, 40) || !craftNativePin(p.CLISHA256, 64) || !craftNativePin(p.EditorBundle, 64) || !filepath.IsAbs(p.CLIPath) || filepath.Clean(p.CLIPath) != p.CLIPath || strings.ContainsRune(p.CLIPath, 0) {
			return nil, errors.New("invalid native document release profile")
		}
		u, err := url.Parse(p.EditorURL)
		sameHost := "/_editor/b/" + p.EditorBundle + "/index.html"
		dedicated := "/b/" + p.EditorBundle + "/index.html"
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !((u.Scheme == "" && u.Host == "" && u.Path == sameHost) || (u.Scheme == "https" && u.Host != "" && u.Path == dedicated)) {
			return nil, errors.New("invalid native editor bundle URL")
		}
		if err = checkCraftNativeCLI(p); err != nil {
			return nil, err
		}
		validator := mooring.NativeCLIValidator{Application: p.Application, BinaryPath: p.CLIPath, SHA256: p.CLISHA256}
		profiles[key] = craftNativeApplication{Store: mooring.NativeStoreConfig{Application: p.Application, EngineRevision: p.EngineRevision, EngineSHA256: p.CLISHA256, EditorBundle: p.EditorBundle, Validate: validator.Validate}, EditorURL: p.EditorURL}
	}
	return profiles, nil
}
func checkCraftNativeCLI(p CraftNativeDocumentProfile) error {
	f, err := os.Open(p.CLIPath)
	if err != nil {
		return errors.New("native document CLI unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() <= 0 || info.Size() > 512<<20 {
		return errors.New("native document CLI unavailable")
	}
	sum := sha256.New()
	if _, err = io.Copy(sum, io.LimitReader(f, (512<<20)+1)); err != nil || hex.EncodeToString(sum.Sum(nil)) != p.CLISHA256 {
		return errors.New("native document CLI pin mismatch")
	}
	return nil
}
