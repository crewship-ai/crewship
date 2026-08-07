package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Check compares the derived surface against the declaration, the published
// compose example and the published guide. It returns one string per problem,
// empty when everything agrees.
func Check(root string, s *Surface) []string {
	var problems []string
	problems = append(problems, checkPackages(s)...)
	problems = append(problems, checkMethods(s)...)
	problems = append(problems, checkShellouts(s)...)
	problems = append(problems, checkCompose(root)...)
	problems = append(problems, checkDocs(root)...)
	return problems
}

func checkPackages(s *Surface) []string {
	var problems []string
	declared := setOf(declaredPackages)
	for _, p := range s.Packages {
		if !declared[p] {
			problems = append(problems, fmt.Sprintf(
				"package %s imports the Docker SDK client but is not in declaredPackages.\n"+
					"Every package that can hold a daemon connection widens the blast radius of a\n"+
					"compromised handler. Add it deliberately, or route the call through\n"+
					"internal/provider/docker instead.", p))
		}
	}
	present := setOf(s.Packages)
	for _, p := range declaredPackages {
		if !present[p] {
			problems = append(problems, fmt.Sprintf(
				"declaredPackages lists %s, which no longer imports the Docker SDK client. Remove it.", p))
		}
	}
	return problems
}

func checkMethods(s *Surface) []string {
	var problems []string
	declared := map[string]Endpoint{}
	for _, e := range allowList {
		declared[e.Method] = e
	}

	for _, m := range sortedMethodNames(s.Methods) {
		e, ok := declared[m]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s is called but not in the allow-list.\n"+
					"  called from: %s\n"+
					"  first site:  %s\n"+
					"This endpoint is not in the published proxy config, so an instance behind a\n"+
					"socket proxy will get a 403 here. Add it to allowList with the Engine API path\n"+
					"and proxy variables it needs, and to the guide and compose example.",
				m, strings.Join(s.Methods[m], ", "), firstSite(s, m)))
			continue
		}
		if diff := diffSets(s.Methods[m], e.Packages); diff != "" {
			problems = append(problems, fmt.Sprintf(
				"%s: the packages calling it changed.\n  %s", m, diff))
		}
	}

	for _, e := range allowList {
		if _, ok := s.Methods[e.Method]; !ok {
			problems = append(problems, fmt.Sprintf(
				"allowList declares %s but nothing calls it.\n"+
					"We are asking operators to grant %s for an endpoint we do not use. Drop it.",
				e.Method, strings.Join(e.ProxyVars, "+")))
		}
	}
	return problems
}

func checkShellouts(s *Surface) []string {
	var problems []string
	declared := map[string]bool{}
	for _, d := range declaredShellouts {
		declared[d.File] = true
	}
	seen := map[string]bool{}
	for _, sh := range s.Shellouts {
		seen[sh.File] = true
		if !declared[sh.File] {
			problems = append(problems, fmt.Sprintf(
				"%s:%d executes a CLI with DOCKER_HOST pinned but is not in declaredShellouts.\n"+
					"A subprocess that inherits DOCKER_HOST is a second client of the same socket.\n"+
					"The compiler cannot see which endpoints it hits — say so explicitly.",
				sh.File, sh.Line))
		}
	}
	for _, d := range declaredShellouts {
		if !seen[d.File] {
			problems = append(problems, fmt.Sprintf(
				"declaredShellouts lists %s, which no longer executes a daemon-pinned CLI. Remove it.", d.File))
		}
	}
	return problems
}

// proxyVarLine matches an environment entry in the compose file, commented or
// not, with or without a trailing comment: "  CONTAINERS: 1  # ..." and
// "  # COMMIT: 1" both match.
var proxyVarLine = regexp.MustCompile(`^\s*(#\s*)?([A-Z_]+):\s*([01])\s*(#.*)?$`)

// checkCompose asserts the supported deployment grants exactly the permissions
// the derived surface needs, with the devcontainer tier present but commented
// out (it is opt-in) and everything else granted.
func checkCompose(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, composePath))
	if err != nil {
		return []string{fmt.Sprintf("cannot read the supported deployment %s: %v", composePath, err)}
	}

	block, err := serviceBlock(string(data), composeService)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", composePath, err)}
	}

	granted := map[string]bool{}
	commented := map[string]bool{}
	for _, line := range strings.Split(block, "\n") {
		m := proxyVarLine.FindStringSubmatch(line)
		if m == nil || m[3] != "1" {
			continue
		}
		if m[1] != "" {
			commented[m[2]] = true
		} else {
			granted[m[2]] = true
		}
	}

	var problems []string
	for _, name := range sortedKeys(varsForTier(TierCore)) {
		if !granted[name] {
			problems = append(problems, fmt.Sprintf(
				"%s must grant %s: 1 — the core surface needs it.", composePath, name))
		}
	}
	for _, name := range sortedKeys(varsForTier(TierDevcontainer)) {
		if varsForTier(TierCore)[name] {
			continue // already granted at the core tier
		}
		if !commented[name] && !granted[name] {
			problems = append(problems, fmt.Sprintf(
				"%s must offer %s (the devcontainer tier needs it), commented out if opt-in.", composePath, name))
		}
	}
	all := varsForTier("")
	for name := range granted {
		if !all[name] {
			problems = append(problems, fmt.Sprintf(
				"%s grants %s: 1, which the derived surface does not need. Every extra permission\n"+
					"is API we are handing over for nothing.", composePath, name))
		}
	}
	sort.Strings(problems)
	return problems
}

// checkDocs asserts the guide names every endpoint and every proxy variable it
// asks an operator to grant. It is deliberately shallow — it cannot judge
// whether the prose is honest — but it does stop the table going stale.
func checkDocs(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, docsPath))
	if err != nil {
		return []string{fmt.Sprintf("cannot read the published guide %s: %v", docsPath, err)}
	}
	text := string(data)

	var missing []string
	for _, e := range allowList {
		if !strings.Contains(text, e.Method) {
			missing = append(missing, e.Method)
		}
	}
	for name := range varsForTier("") {
		if !strings.Contains(text, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return []string{fmt.Sprintf(
		"%s does not mention: %s.\n"+
			"The guide is what an operator copies. An endpoint missing from it is an endpoint\n"+
			"their proxy will deny.", docsPath, strings.Join(missing, ", "))}
}

// serviceBlock returns the lines of one compose service, from its two-space
// indented key to the next sibling key. Scoping the permission parse to the
// proxy service is what stops an unrelated value elsewhere in the file reading
// as a granted permission.
func serviceBlock(doc, service string) (string, error) {
	lines := strings.Split(doc, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "  "+service+":" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("no service %q", service)
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent <= 2 {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), nil
}

// varsForTier returns the proxy variables needed by a tier. An empty tier
// returns the union across all tiers, including the shellouts.
func varsForTier(tier string) map[string]bool {
	out := map[string]bool{}
	for _, e := range allowList {
		if tier != "" && e.Tier != tier {
			continue
		}
		for _, v := range e.ProxyVars {
			out[v] = true
		}
	}
	for _, d := range declaredShellouts {
		if tier != "" && d.Tier != tier {
			continue
		}
		for _, v := range d.ProxyVars {
			out[v] = true
		}
	}
	return out
}

func firstSite(s *Surface, method string) string {
	for _, c := range s.Calls {
		if c.Method == method {
			return fmt.Sprintf("%s:%d", c.File, c.Line)
		}
	}
	return "(none)"
}

func diffSets(got, want []string) string {
	g, w := setOf(got), setOf(want)
	var added, removed []string
	for _, x := range got {
		if !w[x] {
			added = append(added, x)
		}
	}
	for _, x := range want {
		if !g[x] {
			removed = append(removed, x)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "now also called from "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "no longer called from "+strings.Join(removed, ", "))
	}
	return strings.Join(parts, "; ")
}

func setOf(xs []string) map[string]bool {
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}
