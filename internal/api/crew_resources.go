package api

// Crew container resources — surface what an agent's crew container
// already HAS (datastores + installed CLI tools) so the agent uses
// them directly instead of probing ("let me check if postgres is
// reachable…") or trying to install tools it already has.
//
// Two sources, both already stored on the crew row:
//
//   - Datastores: crews.services_json (sidecar services). Each service
//     attaches to the crew's docker bridge network and resolves by DNS
//     = its service Name, so a service named "postgres" is reachable at
//     host "postgres" on the port it declares. We infer the engine
//     ("postgres"/"redis"/…) from the image reference.
//   - Tools: crews.devcontainer_config features + crews.mise_config
//     tools. A devcontainer feature ref like
//     ghcr.io/devcontainers/features/terraform:1 means the terraform
//     CLI is baked into the image; a mise tool "node" means node is on
//     PATH. We map the common feature-ids / mise tool names to friendly
//     CLI names.
//
// Lenient by design: a missing/blank/malformed config contributes no
// entries rather than erroring the whole resolve. Only a DB failure
// surfaces as an error (the caller then logs + omits the block).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DatastoreCap describes one datastore the crew container can reach.
// Host is the in-network DNS name (== the service Name) and Port is the
// first declared port with any /tcp|/udp suffix stripped.
type DatastoreCap struct {
	Type string `json:"type"` // "postgres" | "redis" | "mysql" | "mongodb" | "other"
	Name string `json:"name"` // service name
	Host string `json:"host"` // in-network DNS host (== Name)
	Port string `json:"port"` // first port, "" when none declared
}

// ToolCap describes one CLI tool installed in the crew container.
type ToolCap struct {
	Type string `json:"type"` // friendly tool id, e.g. "ansible"
	Name string `json:"name"` // display name (same as Type today)
}

// CrewResources is the structured view of a crew's container
// capabilities surfaced to the agent.
type CrewResources struct {
	Datastores []DatastoreCap `json:"datastores"`
	Tools      []ToolCap      `json:"tools"`
}

// ResolveCrewResources reads the crew row and derives its datastores
// (from services_json) and tools (from devcontainer features + mise).
// Returns a non-nil *CrewResources with possibly-empty slices. Only a
// DB error is returned; parse failures of individual config columns are
// swallowed so a single malformed blob never blanks the whole result.
func ResolveCrewResources(ctx context.Context, db *sql.DB, crewID string) (*CrewResources, error) {
	if strings.TrimSpace(crewID) == "" {
		return &CrewResources{Datastores: []DatastoreCap{}, Tools: []ToolCap{}}, nil
	}
	var servicesJSON, devcontainerConfig, miseConfig sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT services_json, devcontainer_config, mise_config
		 FROM crews WHERE id = ? AND deleted_at IS NULL`, crewID).
		Scan(&servicesJSON, &devcontainerConfig, &miseConfig)
	if err != nil {
		if err == sql.ErrNoRows {
			// Not an error condition for the caller — a crewless or
			// missing crew just has no resources to surface.
			return &CrewResources{Datastores: []DatastoreCap{}, Tools: []ToolCap{}}, nil
		}
		return nil, fmt.Errorf("query crew resources for %s: %w", crewID, err)
	}

	res := &CrewResources{
		Datastores: parseDatastores(servicesJSON.String),
		Tools:      parseTools(devcontainerConfig.String, miseConfig.String),
	}
	return res, nil
}

// serviceForResources is the minimal slice of the services_json shape
// we need to derive datastores. Mirrors serviceWire's relevant fields.
type serviceForResources struct {
	Name  string   `json:"name"`
	Image string   `json:"image"`
	Ports []string `json:"ports"`
}

// parseDatastores decodes services_json into DatastoreCap entries.
// Malformed JSON or unnamed services contribute nothing.
func parseDatastores(servicesJSON string) []DatastoreCap {
	out := []DatastoreCap{}
	if strings.TrimSpace(servicesJSON) == "" {
		return out
	}
	var svcs []serviceForResources
	if err := json.Unmarshal([]byte(servicesJSON), &svcs); err != nil {
		return out // lenient: malformed → no datastores
	}
	for _, s := range svcs {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		out = append(out, DatastoreCap{
			Type: inferDatastoreType(s.Image),
			Name: name,
			Host: name, // DNS name on the crew bridge network == service name
			Port: firstPort(s.Ports),
		})
	}
	return out
}

// inferDatastoreType maps a container image to a known datastore engine.
// Substring match on the lowercased image; unknown → "other".
func inferDatastoreType(image string) string {
	img := strings.ToLower(image)
	switch {
	case strings.Contains(img, "postgres"):
		return "postgres"
	case strings.Contains(img, "redis"):
		return "redis"
	case strings.Contains(img, "mysql") || strings.Contains(img, "mariadb"):
		return "mysql"
	case strings.Contains(img, "mongo"):
		return "mongodb"
	default:
		return "other"
	}
}

// firstPort returns the first declared port with any "/tcp" / "/udp"
// suffix stripped. Empty slice → "".
func firstPort(ports []string) string {
	if len(ports) == 0 {
		return ""
	}
	p := strings.TrimSpace(ports[0])
	if i := strings.Index(p, "/"); i >= 0 {
		p = p[:i]
	}
	return p
}

// parseTools collects friendly CLI tool names from devcontainer
// features + mise tools, deduped and sorted for stable output.
func parseTools(devcontainerConfig, miseConfig string) []ToolCap {
	seen := map[string]struct{}{}

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		seen[name] = struct{}{}
	}

	// devcontainer features: keys are OCI refs; derive the feature id
	// and map to a friendly CLI name.
	if strings.TrimSpace(devcontainerConfig) != "" {
		var dc struct {
			Features map[string]json.RawMessage `json:"features"`
		}
		if err := json.Unmarshal([]byte(devcontainerConfig), &dc); err == nil {
			for ref := range dc.Features {
				add(featureToolName(featureID(ref)))
			}
		}
	}

	// mise tools: keys are tool names (e.g. "node", "python").
	if strings.TrimSpace(miseConfig) != "" {
		var mc struct {
			Tools map[string]json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal([]byte(miseConfig), &mc); err == nil {
			for name := range mc.Tools {
				add(miseToolName(name))
			}
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]ToolCap, 0, len(names))
	for _, n := range names {
		out = append(out, ToolCap{Type: n, Name: n})
	}
	return out
}

// featureID extracts the feature short id from an OCI feature ref,
// stripping the registry/repo prefix and any :tag or @digest suffix.
//
//	ghcr.io/devcontainers/features/terraform:1        -> "terraform"
//	ghcr.io/devcontainers-extra/features/ansible:2    -> "ansible"
//	ghcr.io/devcontainers/features/go@sha256:deadbeef -> "go"
func featureID(ref string) string {
	s := strings.TrimSpace(ref)
	if i := strings.LastIndex(s, "@"); i >= 0 { // strip digest
		s = s[:i]
	}
	// strip tag: a colon AFTER the last slash (so we don't eat the
	// "ghcr.io:port" style — there's no port in practice but be safe).
	slash := strings.LastIndex(s, "/")
	if colon := strings.LastIndex(s, ":"); colon > slash {
		s = s[:colon]
	}
	if slash := strings.LastIndex(s, "/"); slash >= 0 {
		s = s[slash+1:]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// featureToolNames maps a devcontainer feature id to the friendly CLI
// name an agent would actually invoke. Unknown ids fall through to the
// id itself (still useful to surface), see featureToolName.
var featureToolNames = map[string]string{
	"common-utils":             "git", // base utils image always ships git
	"git":                      "git",
	"git-lfs":                  "git-lfs",
	"github-cli":               "gh",
	"aws-cli":                  "aws",
	"azure-cli":                "az",
	"google-cloud-cli":         "gcloud",
	"gcloud-cli":               "gcloud",
	"kubectl-helm-minikube":    "kubectl",
	"kubectl":                  "kubectl",
	"helm":                     "helm",
	"docker-in-docker":         "docker",
	"docker-outside-of-docker": "docker",
	"terraform":                "terraform",
	"ansible":                  "ansible",
	"node":                     "node",
	"nodejs":                   "node",
	"python":                   "python",
	"go":                       "go",
	"rust":                     "rust",
	"ruby":                     "ruby",
	"java":                     "java",
	"dotnet":                   "dotnet",
	"php":                      "php",
	"hugo":                     "hugo",
}

// featureToolName resolves a feature id to its friendly CLI name,
// falling back to the id itself when unmapped.
func featureToolName(id string) string {
	if name, ok := featureToolNames[id]; ok {
		return name
	}
	return id
}

// miseToolName normalises a mise tool key to a friendly CLI name. Most
// are identity; a few common aliases get folded.
func miseToolName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "golang":
		return "go"
	case "nodejs":
		return "node"
	default:
		return n
	}
}

// datastoreLabels renders the engine type as a human label for the
// system-prompt block. Unknown ("other") falls back to the service name
// at the call site.
var datastoreLabels = map[string]string{
	"postgres": "Postgres",
	"redis":    "Redis",
	"mysql":    "MySQL",
	"mongodb":  "MongoDB",
}

// buildContainerResourcesBlock renders the [CONTAINER RESOURCES] system
// prompt section. Returns "" when the crew has neither datastores nor
// tools (so the block is omitted entirely rather than rendering an empty
// husk).
func buildContainerResourcesBlock(res *CrewResources) string {
	if res == nil || (len(res.Datastores) == 0 && len(res.Tools) == 0) {
		return ""
	}

	var b strings.Builder
	b.WriteString("[CONTAINER RESOURCES]\n")
	b.WriteString("Your crew's container has these resources ready — USE them directly, do not probe or install:\n")

	if len(res.Datastores) > 0 {
		b.WriteString("Datastores:\n")
		for _, d := range res.Datastores {
			label := datastoreLabels[d.Type]
			if label == "" {
				label = d.Name
			}
			if d.Port != "" {
				fmt.Fprintf(&b, "- %s — host: %s, port: %s\n", label, d.Host, d.Port)
			} else {
				fmt.Fprintf(&b, "- %s — host: %s\n", label, d.Host)
			}
		}
	}

	if len(res.Tools) > 0 {
		names := make([]string, 0, len(res.Tools))
		for _, t := range res.Tools {
			names = append(names, t.Name)
		}
		fmt.Fprintf(&b, "Tools (CLIs installed): %s\n", strings.Join(names, ", "))
	}

	if len(res.Datastores) > 0 {
		b.WriteString("To use a datastore from a routine, declare it in the routine's `resources.datastores`; connect via the host/port above.\n")
	}

	b.WriteString("[END CONTAINER RESOURCES]")
	return b.String()
}
