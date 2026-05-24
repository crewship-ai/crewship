package seeddata

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/statuses"
)

// LabelDef defines a label to seed.
type LabelDef struct {
	Name  string `json:"name"  yaml:"name"`
	Color string `json:"color" yaml:"color"`
}

// ProjectDef defines a project to seed.
type ProjectDef struct {
	Name     string `yaml:"name"`
	Color    string `yaml:"color"`
	Icon     string `yaml:"icon"`
	Status   string `yaml:"status"`
	Priority string `yaml:"priority"`
}

// IssueDef defines an issue to seed. REAL executable tasks that agents
// run inside containers — keep simple, idempotent, safe to run 100x.
type IssueDef struct {
	CrewSlug    string `yaml:"crew_slug"`
	Assignee    string `yaml:"assignee"` // agent slug — resolved to ID during seed
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Priority    string `yaml:"priority,omitempty"`
	Project     string `yaml:"project,omitempty"`      // project name (resolved to ID during seed)
	TargetState string `yaml:"target_state,omitempty"` // final status after creation (empty = BACKLOG)
	Comment     string `yaml:"comment,omitempty"`
}

// issuesBundle is the on-disk shape of builtin/issues.yaml — three
// catalogues that ship together because they're cross-referenced (issues
// point at project names and assignees defined alongside).
type issuesBundle struct {
	Labels   []LabelDef   `yaml:"labels"`
	Projects []ProjectDef `yaml:"projects"`
	Issues   []IssueDef   `yaml:"issues"`
}

// Labels — the canonical label set seeded into a fresh workspace.
//
// Loaded from builtin/issues.yaml at init time. Migrated from a
// Go-literal list in F2 step 6 alongside Projects + Issues since the
// three catalogues are cross-referenced.
var Labels = bundleLabels()

// Projects — the demo project catalogue.
var Projects = bundleProjects()

// Issues — the demo issue catalogue (REAL executable tasks).
var Issues = bundleIssues()

// loadedBundle caches the single parse of builtin/issues.yaml so
// Labels / Projects / Issues don't each re-parse the same file.
var loadedBundle = mustLoadIssuesBundle()

func mustLoadIssuesBundle() issuesBundle {
	data, err := builtinFS.ReadFile("builtin/issues.yaml")
	if err != nil {
		panic(fmt.Sprintf("seeddata: read builtin/issues.yaml: %v", err))
	}
	var doc issuesBundle
	if err := yaml.Unmarshal(data, &doc); err != nil {
		panic(fmt.Sprintf("seeddata: parse builtin/issues.yaml: %v", err))
	}
	return doc
}

func bundleLabels() []LabelDef     { return loadedBundle.Labels }
func bundleProjects() []ProjectDef { return loadedBundle.Projects }
func bundleIssues() []IssueDef     { return loadedBundle.Issues }

// StatusPath returns the sequence of status transitions needed to reach
// target from BACKLOG. It delegates to StatusPathFrom so that seed creation
// and nuke cleanup share the same DAG defined in validIssueTransitions.
func StatusPath(target string) []string {
	return StatusPathFrom("BACKLOG", target)
}

// validIssueTransitions references the canonical transition map from the
// statuses package — single source of truth.
var validIssueTransitions = statuses.ValidIssueTransitions

// StatusPathFrom returns the shortest sequence of status transitions needed
// to move an issue from current to target, using the server-side issue
// status DAG. The returned slice does NOT include current and DOES include
// target as its final element. If current == target, returns an empty
// non-nil slice. Returns nil when no valid path exists.
func StatusPathFrom(current, target string) []string {
	if current == target {
		return []string{}
	}
	if _, ok := validIssueTransitions[current]; !ok {
		return nil
	}
	if _, ok := validIssueTransitions[target]; !ok {
		return nil
	}
	// BFS over the transition graph.
	type node struct {
		status string
		path   []string
	}
	visited := map[string]bool{current: true}
	queue := []node{{status: current, path: nil}}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, next := range validIssueTransitions[n.status] {
			if visited[next] {
				continue
			}
			visited[next] = true
			nextPath := make([]string, len(n.path)+1)
			copy(nextPath, n.path)
			nextPath[len(n.path)] = next
			if next == target {
				return nextPath
			}
			queue = append(queue, node{status: next, path: nextPath})
		}
	}
	return nil
}
