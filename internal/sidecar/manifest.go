package sidecar

import (
	"encoding/json"
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

type ManifestPackages struct {
	Apt []string `json:"apt"`
	Npm []string `json:"npm"`
	Pip []string `json:"pip"`
}

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
		return &CrewManifest{Version: 1}, nil
	}
	return &m, nil
}

func writeManifest(m *CrewManifest) error {
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, data, 0644)
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
		Packages      *ManifestPackages    `json:"packages"`
		Credentials   []ManifestCredEntry  `json:"credentials"`
		SetupCommands []string             `json:"setup_commands"`
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
		m = CrewManifest{Version: 1}
	} else {
		if jsonErr := json.Unmarshal(data, &m); jsonErr != nil {
			m = CrewManifest{Version: 1}
		}
	}

	if patch.Packages != nil {
		m.Packages.Apt = mergeUnique(m.Packages.Apt, patch.Packages.Apt)
		m.Packages.Npm = mergeUnique(m.Packages.Npm, patch.Packages.Npm)
		m.Packages.Pip = mergeUnique(m.Packages.Pip, patch.Packages.Pip)
	}
	if len(patch.Credentials) > 0 {
		m.Credentials = append(m.Credentials, patch.Credentials...)
	}
	if len(patch.SetupCommands) > 0 {
		m.SetupCommands = mergeUnique(m.SetupCommands, patch.SetupCommands)
	}

	outData, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.WriteFile(manifestPath, outData, 0644); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSONResponse(w, http.StatusOK, m)
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
