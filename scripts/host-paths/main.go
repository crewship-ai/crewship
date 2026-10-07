// host-paths prevents new independent host filesystem roots in product code.
// Existing sites are individually recorded as debt or container-side paths.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const allowlistPath = "scripts/host-paths/allowlist.json"

type exception struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// Keys identify syntax and containing function, not source line numbers.
func scan(path string, source []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	dotOS := false
	for _, imp := range f.Imports {
		pkg, _ := strconv.Unquote(imp.Path.Value)
		if pkg != "os" {
			continue
		}
		name := "os"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			dotOS = true
		} else {
			aliases[name] = true
		}
	}
	var keys []string
	counts := map[string]int{}
	for _, decl := range f.Decls {
		context := "package"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			context = fn.Name.Name
			if fn.Recv != nil {
				var b bytes.Buffer
				_ = format.Node(&b, fset, fn.Recv.List[0].Type)
				context = b.String() + "." + context
			}
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			var kind, value string
			switch n := n.(type) {
			case *ast.CallExpr:
				method := ""
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] {
						method = sel.Sel.Name
					}
				} else if id, ok := n.Fun.(*ast.Ident); ok && dotOS {
					method = id.Name
				}
				if method == "UserHomeDir" || method == "TempDir" || method == "Getwd" {
					kind, value = "os-root", method
				}
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					break
				}
				v, e := strconv.Unquote(n.Value)
				if e != nil {
					break
				}
				for _, prefix := range []string{"/tmp", "/var/lib/crewship", "/var/log/crewship"} {
					if v == prefix || strings.HasPrefix(v, prefix+"/") {
						kind, value = "literal", v
						break
					}
				}
			}
			if kind != "" {
				key := path + "|" + context + "|" + kind + "|" + value
				counts[key]++
				keys = append(keys, fmt.Sprintf("%s|%d", key, counts[key]))
			}
			return true
		})
	}
	sort.Strings(keys)
	return keys, nil
}

func productFile(path string) bool {
	if !strings.HasPrefix(path, "internal/") && !strings.HasPrefix(path, "cmd/") {
		return false
	}
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/testdata/") {
		return false
	}
	switch path {
	case "internal/config/paths.go", "internal/config/resolve_paths.go", "internal/database/datadir.go":
		return false
	}
	return true
}

func inventory(root string) ([]string, error) {
	var keys []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !productFile(rel) {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found, err := scan(rel, b)
			keys = append(keys, found...)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func validate(keys []string, allowed []exception) error {
	actual := map[string]bool{}
	for _, k := range keys {
		actual[k] = true
	}
	seen := map[string]bool{}
	var problems []string
	for _, e := range allowed {
		if seen[e.Key] || strings.TrimSpace(e.Reason) == "" {
			problems = append(problems, "invalid exception: "+e.Key)
		}
		seen[e.Key] = true
		if !actual[e.Key] {
			problems = append(problems, "stale exception: "+e.Key)
		}
	}
	for _, k := range keys {
		if !seen[k] {
			problems = append(problems, "new host path root: "+k)
		}
	}
	if len(problems) != 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

// Once a baseline exists, additions are refused against the PR target. On
// initial introduction, every exception must describe syntax already in base.
func checkBase(root, base string, allowed []exception) error {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		return cmd.Output()
	}
	if _, err := git("rev-parse", "--verify", base+"^{commit}"); err != nil {
		return fmt.Errorf("cannot verify base revision: %w", err)
	}
	old := map[string]bool{}
	if b, err := git("show", base+":"+allowlistPath); err == nil {
		var entries []exception
		if err := json.Unmarshal(b, &entries); err != nil {
			return err
		}
		for _, e := range entries {
			old[e.Key] = true
		}
	} else {
		paths := map[string]bool{}
		for _, e := range allowed {
			paths[strings.SplitN(e.Key, "|", 2)[0]] = true
		}
		for path := range paths {
			b, err := git("show", base+":"+path)
			if err != nil {
				return fmt.Errorf("exception has no existing base file %s: %w", path, err)
			}
			keys, err := scan(path, b)
			if err != nil {
				return err
			}
			for _, k := range keys {
				old[k] = true
			}
		}
	}
	for _, e := range allowed {
		if !old[e.Key] {
			return fmt.Errorf("allowlist expansion requires removing the new independent root: %s", e.Key)
		}
	}
	return nil
}

func run() error {
	root := flag.String("root", ".", "repository root")
	base := flag.String("base", "", "PR target revision; enforce shrinking allowlist")
	flag.Parse()
	keys, err := inventory(*root)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(*root, allowlistPath))
	if err != nil {
		return err
	}
	var allowed []exception
	if err := json.Unmarshal(b, &allowed); err != nil {
		return err
	}
	if err := validate(keys, allowed); err != nil {
		return err
	}
	if *base != "" {
		if err := checkBase(*root, *base, allowed); err != nil {
			return err
		}
	}
	fmt.Printf("host-paths: %d existing exceptions; no new roots or stale exceptions\n", len(allowed))
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
