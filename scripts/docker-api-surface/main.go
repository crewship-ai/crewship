// Command docker-api-surface pins the Docker Engine API surface the Crewship
// server can reach.
//
// The server talks to Docker directly, which is root-equivalent on the host. An
// operator can narrow that with a socket proxy — we honour DOCKER_HOST — but a
// proxy is only as good as the endpoint list behind it, and an endpoint list is
// exactly the kind of documentation that rots the first time somebody adds a
// call. This command re-derives the list from the source on every run and fails
// when it stops matching the declaration in allowlist.go.
//
// It fails in both directions on purpose. A new call nobody listed means the
// published proxy config would 403 in production; a listed endpoint with no
// call site means we are telling operators to grant a permission we do not
// need. Both are wrong, and the second is the one nobody would ever notice.
//
// Run from the repository root:
//
//	go run ./scripts/docker-api-surface           # check, exit 1 on drift
//	go run ./scripts/docker-api-surface -list     # print the derived surface
//
// See docs/guides/docker-socket-proxy.mdx for what this surface does and does
// not buy you.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	list := flag.Bool("list", false, "print the derived surface instead of checking it")
	flag.Parse()

	surface, err := Scan(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docker-api-surface:", err)
		os.Exit(2)
	}

	if *list {
		printSurface(os.Stdout, surface)
		return
	}

	problems := Check(*root, surface)
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "docker-api-surface: %d problem(s)\n\n", len(problems))
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+strings.ReplaceAll(p, "\n", "\n  "))
		}
		fmt.Fprintf(os.Stderr, "\nThe Docker surface is published as %s and %s.\n", docsPath, composePath)
		fmt.Fprintln(os.Stderr, "Update scripts/docker-api-surface/allowlist.go and both of those together,")
		fmt.Fprintln(os.Stderr, "or drop the call — every added endpoint widens what a socket proxy must allow.")
		os.Exit(1)
	}

	fmt.Printf("docker-api-surface: %d endpoints across %d packages, %d shellout(s), docs and compose in sync\n",
		len(allowList), len(surface.Packages), len(surface.Shellouts))
}

func printSurface(w *os.File, s *Surface) {
	fmt.Fprintf(w, "packages importing the Docker SDK (%d):\n", len(s.Packages))
	for _, p := range s.Packages {
		fmt.Fprintf(w, "  %s\n", p)
	}
	fmt.Fprintf(w, "\nSDK methods reached (%d):\n", len(s.Methods))
	for _, m := range sortedMethodNames(s.Methods) {
		fmt.Fprintf(w, "  %-20s %s\n", m, strings.Join(s.Methods[m], ", "))
	}
	fmt.Fprintf(w, "\ncall sites (%d):\n", len(s.Calls))
	for _, c := range s.Calls {
		fmt.Fprintf(w, "  %-20s %s:%d (%s)\n", c.Method, c.File, c.Line, c.Receiver)
	}
	fmt.Fprintf(w, "\ndaemon-pinned CLI shellouts (%d):\n", len(s.Shellouts))
	for _, sh := range s.Shellouts {
		fmt.Fprintf(w, "  %s:%d\n", sh.File, sh.Line)
	}
}

func sortedMethodNames(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
