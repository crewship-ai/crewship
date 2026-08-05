// Command docs-surface-check verifies the agent-readable Mintlify surface.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type config struct {
	Contextual *struct {
		Options []json.RawMessage `json:"options"`
	} `json:"contextual"`
	Navigation json.RawMessage `json:"navigation"`
}

var frontmatter = regexp.MustCompile(`(?ms)^---\s*\n(.*?)\n---`)
var titleLine = regexp.MustCompile(`(?m)^title:\s*["']?([^"'\n]+)`)
var descriptionLine = regexp.MustCompile(`(?m)^description:\s*["']?([^"'\n]+)`)
var llmsLink = regexp.MustCompile(`(?m)^- \[[^]]+\]\([^)]*\)`)

func main() {
	root := flag.String("root", ".", "repository root")
	baseURL := flag.String("url", "https://docs.crewship.ai", "deployed docs URL")
	flag.Parse()
	data, err := os.ReadFile(filepath.Join(*root, "docs/docs.json"))
	if err != nil {
		fail(err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fail(err)
	}
	total, good, bad := descriptionQuality(*root)
	fmt.Printf("docs-surface-check: description quality %d/%d good, %d restate their title\n", good, total, bad)
	if cfg.Contextual == nil || len(cfg.Contextual.Options) == 0 {
		fail(fmt.Errorf("docs/docs.json must declare contextual.options"))
	}

	declared := navigationPages(cfg.Navigation)
	if len(declared) == 0 {
		fail(fmt.Errorf("docs/docs.json declares no navigation pages"))
	}
	missing := []string{}
	for _, page := range declared {
		if !fileExists(filepath.Join(*root, "docs", page+".mdx")) && !fileExists(filepath.Join(*root, "docs", page+".md")) {
			missing = append(missing, page)
		}
	}
	if len(missing) > 0 {
		fail(fmt.Errorf("navigation pages missing from docs tree: %s", strings.Join(missing, ", ")))
	}
	llms, err := fetch(*baseURL + "/llms.txt")
	if err != nil {
		fail(err)
	}
	full, err := fetch(*baseURL + "/llms-full.txt")
	if err != nil {
		fail(err)
	}
	_ = full // Status and availability are the contract; the full index has no page list to count.
	served := len(llmsLink.FindAllString(llms, -1))
	if served < len(declared) {
		fail(fmt.Errorf("llms.txt lists %d pages, docs.json declares %d", served, len(declared)))
	}

	fmt.Printf("docs-surface-check: contextual options=%d, navigation pages=%d, llms pages=%d\n", len(cfg.Contextual.Options), len(declared), served)
}

func navigationPages(raw json.RawMessage) []string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		case string:
			if strings.Contains(x, "/") || strings.HasPrefix(x, "cli") || strings.HasPrefix(x, "guides") || strings.HasPrefix(x, "api-reference") || strings.HasPrefix(x, "manifest") || x == "index" || x == "quickstart" || x == "concepts" {
				seen[x] = true
			}
		}
	}
	walk(value)
	pages := make([]string, 0, len(seen))
	for page := range seen {
		pages = append(pages, page)
	}
	sort.Strings(pages)
	return pages
}

func fetch(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func descriptionQuality(root string) (total, good, bad int) {
	_ = filepath.Walk(filepath.Join(root, "docs"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".mdx") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		match := frontmatter.FindStringSubmatch(string(data))
		if len(match) != 2 {
			return nil
		}
		title := titleLine.FindStringSubmatch(match[1])
		desc := descriptionLine.FindStringSubmatch(match[1])
		if len(title) != 2 || len(desc) != 2 {
			return nil
		}
		total++
		if strings.TrimSpace(strings.Trim(desc[1], " \t\"'")) == strings.TrimSpace(strings.Trim(title[1], " \t\"'")) {
			bad++
		} else {
			good++
		}
		return nil
	})
	return
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func fail(err error)              { fmt.Fprintln(os.Stderr, "docs-surface-check:", err); os.Exit(1) }
