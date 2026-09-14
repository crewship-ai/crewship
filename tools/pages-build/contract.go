package pageprofile

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/pages"
)

const sourcePathPattern = `^[A-Za-z0-9_./@+-]+$`

var sourcePath = regexp.MustCompile(sourcePathPattern)
var customConfig = regexp.MustCompile(`(^|/)(vite|postcss|tailwind)\.config\.`)

// ValidateSourcePaths describes the installed compiler profile, not the general
// portable source codec. Reject unsupported names while saving, before Docker.
func ValidateSourcePaths(p *pages.SourceProject) error {
	if p == nil {
		return fmt.Errorf("a Page source project is required")
	}
	for _, file := range p.Files {
		if !sourcePath.MatchString(file.Path) {
			return fmt.Errorf("unsupported source path %q: the build profile accepts ASCII letters, digits, _ . / @ + -", file.Path)
		}
		for _, part := range strings.Split(file.Path, "/") {
			if strings.HasPrefix(part, ".") || part == "node_modules" {
				return fmt.Errorf("unsupported source path %q: hidden directories/files and node_modules are not supported", file.Path)
			}
		}
		if customConfig.MatchString(strings.ToLower(file.Path)) {
			return fmt.Errorf("unsupported build configuration %q: use the installed Pages profile", file.Path)
		}
	}
	return nil
}

// ValidateDependencies gives immediate diagnostics; the isolated worker repeats
// these checks against its own installed dependency set and exact lockfile.
func ValidateDependencies(p *pages.SourceProject) error {
	if err := ValidateSourcePaths(p); err != nil {
		return err
	}
	expected, _ := files.ReadFile("package.json")
	lock, _ := files.ReadFile("pnpm-lock.yaml")
	var want map[string]json.RawMessage
	if err := json.Unmarshal(expected, &want); err != nil {
		return err
	}
	for _, file := range p.Files {
		if file.Path != "package.json" && file.Path != "pnpm-lock.yaml" {
			continue
		}
		data, err := file.Bytes()
		if err != nil {
			return err
		}
		if file.Path == "pnpm-lock.yaml" {
			if !bytes.Equal(data, lock) {
				return fmt.Errorf("pnpm-lock.yaml differs from the installed Pages profile; restore the supplied lockfile")
			}
			continue
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(data, &got); err != nil {
			return fmt.Errorf("package.json: %w", err)
		}
		for _, field := range []string{"dependencies", "devDependencies"} {
			var actual, required any
			if err := json.Unmarshal(got[field], &actual); err != nil {
				return fmt.Errorf("package.json: %s must match the installed profile", field)
			}
			if err := json.Unmarshal(want[field], &required); err != nil {
				return err
			}
			a, _ := json.Marshal(actual)
			b, _ := json.Marshal(required)
			if !bytes.Equal(a, b) {
				return fmt.Errorf("package.json: %s differs from the installed Pages profile", field)
			}
		}
	}
	return nil
}

// Contract is returned to authors so unsupported projects need not be discovered
// by trial and error. The image digest on each build identifies the actual SDK.
func Contract() map[string]any {
	manifest, _ := files.ReadFile("package.json")
	lock, _ := files.ReadFile("pnpm-lock.yaml")
	var packages map[string]any
	_ = json.Unmarshal(manifest, &packages)
	return map[string]any{
		"runtime": pages.SourceProjectRuntime, "profile_sha256": Fingerprint(),
		"dependencies": packages["dependencies"], "dev_dependencies": packages["devDependencies"],
		"path_pattern": sourcePathPattern, "hidden_paths": false,
		"custom_build_configuration": false, "package_scripts": false,
		"external_network": false, "public_directory": false,
		"entrypoint": "src/main.tsx", "sdk_module": "@crewship/pages",
		"sdk_sha256":         fmt.Sprintf("%x", sha256.Sum256([]byte(SDKSource()))),
		"lockfile_sha256":    fmt.Sprintf("%x", sha256.Sum256(lock)),
		"supported_browsers": []string{"desktop Chromium (Chrome, Edge)"},
	}
}

// Fingerprint binds the installed compiler implementation, SDK and dependency
// manifests. Changing any part requires a matching tools image from the release.
func Fingerprint() string {
	hash := sha256.New()
	for _, name := range []string{"package.json", "pnpm-lock.yaml", "sdk.ts", "build.mjs"} {
		data, _ := files.ReadFile(name)
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write(data)
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
