// agents-invariants turns the checkable half of AGENTS.md's "NEVER DO" list
// into a check.
//
// Why this exists. Every document in this repository that stayed true has a
// gate behind it — docs-inventory, docs-surface-check, the OpenAPI freshness
// check. The ones that drifted had none. AGENTS.md had none, and by
// 2026-08-11 two of its nine verifiable claims were false: it described the
// error convention as RFC 7807 when ~83% of the code writes {"error": …}, and
// it pointed at .claude/context/prd/ as the place for design context while the
// entire release-1.0 body of work sat in docs/prd/.
//
// Five of the nine "NEVER DO" entries are statements about the tree rather
// than about behaviour, so they can be checked rather than promised. The other
// four — run the verification loop, do not discard WIP, claim before you work,
// do not commit secrets — are about what a person does, and gitleaks already
// covers the last one. They stay prose, honestly labelled as such.
//
// Usage: go run ./scripts/agents-invariants
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type violation struct {
	rule   string
	detail string
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	var found []violation
	for _, check := range []func(string) []violation{
		noAPIRoutesUnderApp,
		noSqlite3DriverName,
		noNpmOrYarnLockfile,
		sidecarAndAgentUIDsUnchanged,
	} {
		found = append(found, check(root)...)
	}

	if len(found) > 0 {
		for _, v := range found {
			fmt.Fprintf(os.Stderr, "AGENTS.md invariant violated: %s\n  %s\n", v.rule, v.detail)
		}
		os.Exit(1)
	}
	fmt.Println("agents-invariants: 4 checkable NEVER DO entries hold")
}

// "Never add API routes under app/ — static export silently drops them."
func noAPIRoutesUnderApp(root string) []violation {
	var out []violation
	_ = filepath.Walk(filepath.Join(root, "app"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // a missing app/ is not this check's problem
		}
		base := filepath.Base(path)
		if base == "route.ts" || base == "route.tsx" || base == "route.js" {
			out = append(out, violation{
				rule:   "never add API routes under app/",
				detail: path + " — the static export drops it silently, so it 404s in production only",
			})
		}
		return nil
	})
	return out
}

// `Never use "sqlite3" as the driver name — modernc.org/sqlite registers "sqlite".`
func noSqlite3DriverName(root string) []violation {
	pattern := regexp.MustCompile(`sql\.Open\(\s*"sqlite3"`)
	var out []violation
	for _, path := range goFiles(root) {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if pattern.Match(body) {
			out = append(out, violation{
				rule:   `never use "sqlite3" as the driver name`,
				detail: path + ` — modernc.org/sqlite registers "sqlite"; "sqlite3" panics at open`,
			})
		}
	}
	return out
}

// "Never use npm/yarn — pnpm only." A lockfile is the durable evidence; a
// stray `npm install` leaves one behind and the next install diverges.
func noNpmOrYarnLockfile(root string) []violation {
	var out []violation
	for _, name := range []string{"package-lock.json", "yarn.lock", "npm-shrinkwrap.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			out = append(out, violation{
				rule:   "never use npm/yarn — pnpm only",
				detail: name + " exists; pnpm-lock.yaml is the only lockfile this repo resolves from",
			})
		}
	}
	return out
}

// "Never change sidecar UID (1002) or agent UID (1001) — it's a security boundary."
//
// Checked as presence rather than absence: the numbers must still be there.
// A rename or a refactor that drops them is the change this guards, and it
// would otherwise pass every other test in the repository.
func sidecarAndAgentUIDsUnchanged(root string) []violation {
	want := map[string]string{
		"1001": "agent UID",
		"1002": "sidecar UID",
	}
	seen := map[string]bool{}
	for _, path := range goFiles(root) {
		if !strings.Contains(path, "orchestrator") && !strings.Contains(path, "sidecar") && !strings.Contains(path, "provider") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for uid := range want {
			if strings.Contains(string(body), uid) {
				seen[uid] = true
			}
		}
	}
	var out []violation
	for uid, what := range want {
		if !seen[uid] {
			out = append(out, violation{
				rule:   "never change sidecar UID (1002) or agent UID (1001)",
				detail: fmt.Sprintf("%s (%s) appears nowhere in orchestrator/sidecar/provider — it is a security boundary", what, uid),
			})
		}
	}
	return out
}

func goFiles(root string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", "web", "out", ".next":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	return out
}
