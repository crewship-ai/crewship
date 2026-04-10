package sidecar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

const manifestPath = "/crew/manifest.json"

// CrewManifest tracks installed packages, credentials, and setup commands
// for container reproducibility. The Lead agent updates this as it provisions
// the environment; on restart the manifest is replayed.
type CrewManifest struct {
	Version       int                 `json:"version"`
	Packages      ManifestPackages    `json:"packages"`
	Credentials   []ManifestCredEntry `json:"credentials"`
	SetupCommands []string            `json:"setup_commands"`
}

// ManifestPackages lists packages installed in the container by package manager.
type ManifestPackages struct {
	Apt []string `json:"apt"`
	Npm []string `json:"npm"`
	Pip []string `json:"pip"`
}

// ManifestCredEntry records a credential assigned to an agent in the manifest.
type ManifestCredEntry struct {
	Name  string `json:"name"`
	Agent string `json:"agent"`
	Type  string `json:"type"`
}

var manifestMu sync.Mutex

func readManifest() (*CrewManifest, error) {
	manifestMu.Lock()
	defer manifestMu.Unlock()

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &CrewManifest{Version: 1}, nil
		}
		return nil, err
	}

	var m CrewManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", manifestPath, err)
	}
	return &m, nil
}

func writeManifest(m *CrewManifest) error {
	dir := filepath.Dir(manifestPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to temp file then rename to avoid partial reads
	tmp, err := os.CreateTemp(dir, ".manifest.tmp")
	if err != nil {
		return fmt.Errorf("create temp manifest: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op if rename succeeded
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp manifest: %w", err)
	}
	return os.Rename(tmpName, manifestPath)
}

// handleGetManifest returns the current crew manifest.
func (s *Server) handleGetManifest(w http.ResponseWriter, _ *http.Request) {
	m, err := readManifest()
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSONResponse(w, http.StatusOK, m)
}

// handleUpdateManifest merges additions into the existing manifest.
// Agents POST partial updates: new packages, credentials, or setup commands are appended.
func (s *Server) handleUpdateManifest(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		Packages      *ManifestPackages   `json:"packages"`
		Credentials   []ManifestCredEntry `json:"credentials"`
		SetupCommands []string            `json:"setup_commands"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	manifestMu.Lock()
	defer manifestMu.Unlock()

	data, err := os.ReadFile(manifestPath)
	var m CrewManifest
	if err != nil {
		if !os.IsNotExist(err) {
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("read manifest: %s", err.Error())})
			return
		}
		m = CrewManifest{Version: 1}
	} else {
		if jsonErr := json.Unmarshal(data, &m); jsonErr != nil {
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("parse manifest: %s", jsonErr.Error())})
			return
		}
	}

	if patch.Packages != nil {
		m.Packages.Apt = mergeUnique(m.Packages.Apt, patch.Packages.Apt)
		m.Packages.Npm = mergeUnique(m.Packages.Npm, patch.Packages.Npm)
		m.Packages.Pip = mergeUnique(m.Packages.Pip, patch.Packages.Pip)
	}
	if len(patch.Credentials) > 0 {
		m.Credentials = mergeCredentials(m.Credentials, patch.Credentials)
	}
	if len(patch.SetupCommands) > 0 {
		m.SetupCommands = mergeUnique(m.SetupCommands, patch.SetupCommands)
	}

	if err := writeManifest(&m); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSONResponse(w, http.StatusOK, m)
}

func mergeCredentials(existing, additions []ManifestCredEntry) []ManifestCredEntry {
	seen := make(map[string]bool, len(existing))
	for _, c := range existing {
		seen[c.Name+"|"+c.Agent] = true
	}
	for _, c := range additions {
		key := c.Name + "|" + c.Agent
		if !seen[key] {
			existing = append(existing, c)
			seen[key] = true
		}
	}
	return existing
}

func mergeUnique(existing, additions []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, v := range existing {
		seen[v] = true
	}
	for _, v := range additions {
		if !seen[v] {
			existing = append(existing, v)
			seen[v] = true
		}
	}
	return existing
}
