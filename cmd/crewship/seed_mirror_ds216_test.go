package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The mirrored claude.ai design system (showcase/mirror/ds216) is an artifact
// project whose manifest holds design-system.json, so it opens as a Design in
// Library mode. These tests pin that its manifest is the readable files/
// folder verbatim, that its native token file carries every original token,
// and that the real create route accepts it once and leaves it alone after.

const mirrorDS216Dir = "seeddata/showcase/mirror/ds216"

type mirrorDS216File struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Content   string `json:"content"`
}

type mirrorDS216Manifest struct {
	EntryPoint string            `json:"entrypoint"`
	Files      []mirrorDS216File `json:"files"`
	Message    string            `json:"message"`
}

func mirrorDS216Item(t *testing.T) seeddata.ShowcaseArtifactDef {
	t.Helper()
	for _, item := range seeddata.ShowcaseArtifacts {
		if item.File == "mirror/ds216/manifest.json" {
			return item
		}
	}
	t.Fatal("the ds216 mirror is not registered in ShowcaseArtifacts")
	return seeddata.ShowcaseArtifactDef{}
}

func mirrorDS216Load(t *testing.T) mirrorDS216Manifest {
	t.Helper()
	raw := seeddata.ShowcaseFile("mirror/ds216/manifest.json")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		if key != "entrypoint" && key != "files" && key != "message" {
			t.Errorf("manifest field %q is refused by POST /api/v1/pages/artifacts", key)
		}
	}
	var m mirrorDS216Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m mirrorDS216Manifest) file(t *testing.T, name string) string {
	t.Helper()
	for _, f := range m.Files {
		if f.Path == name {
			return f.Content
		}
	}
	t.Fatalf("manifest has no %s", name)
	return ""
}

func TestMirrorDS216_Registered(t *testing.T) {
	t.Parallel()
	item := mirrorDS216Item(t)
	if item.Slug != "ref-design-system" || item.Name != "Template Crewship Design System" || len(item.Data) != 0 {
		t.Errorf("catalogue entry = %+v", item)
	}
	seen := map[string]int{}
	for _, a := range seeddata.ShowcaseArtifacts {
		seen[a.Slug]++
	}
	for _, d := range seeddata.ShowcaseDesigns {
		seen[d.Slug]++
	}
	for _, c := range seeddata.ShowcaseCrafts {
		seen[c.Slug]++
	}
	for _, p := range seeddata.Pages {
		seen[p.Slug]++
	}
	if seen[item.Slug] != 1 {
		t.Errorf("slug %s is used %d times", item.Slug, seen[item.Slug])
	}
}

// The manifest is generated from files/; a hand edit to one of them must not
// leave the seeded copy behind.
func TestMirrorDS216_ManifestIsTheFilesFolder(t *testing.T) {
	t.Parallel()
	m := mirrorDS216Load(t)
	root := filepath.Join(mirrorDS216Dir, "files")
	var onDisk []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			onDisk = append(onDisk, filepath.ToSlash(rel))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var listed []string
	media := map[string]string{".html": "text/html", ".json": "application/json", ".md": "text/markdown"}
	total := 0
	for _, f := range m.Files {
		listed = append(listed, f.Path)
		if path.Clean(f.Path) != f.Path || path.IsAbs(f.Path) || strings.HasPrefix(f.Path, "../") || strings.ContainsAny(f.Path, "\\\x00?#") {
			t.Errorf("%s: path the VersionStore refuses", f.Path)
		}
		if f.MediaType != media[path.Ext(f.Path)] {
			t.Errorf("%s: media type %q", f.Path, f.MediaType)
		}
		disk, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil || string(disk) != f.Content {
			t.Errorf("%s: manifest content differs from files/%s (regenerate manifest.json, see SOURCE.md)", f.Path, f.Path)
		}
		total += len(f.Content)
	}
	sort.Strings(onDisk)
	sort.Strings(listed)
	if strings.Join(onDisk, ",") != strings.Join(listed, ",") {
		t.Errorf("manifest files %v, files/ holds %v", listed, onDisk)
	}
	if m.EntryPoint != "index.html" || m.file(t, "index.html") == "" {
		t.Errorf("entry point %q", m.EntryPoint)
	}
	if total > 2<<20 {
		t.Errorf("mirror is %d bytes; keep it under 2 MB", total)
	}
	if m.Message != "První malý testovací systém: barvy, písmo, rozestupy, rádiusy, obálka." {
		t.Errorf("message %q is not the original's last change note", m.Message)
	}
}

// originalTokens is the part of the claude.ai tokens.json the mirror maps.
type originalTokens struct {
	Name  string `json:"name"`
	Color struct {
		Themes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"themes"`
		Tokens []struct {
			Name  string            `json:"name"`
			Value map[string]string `json:"value"`
			Usage string            `json:"usage"`
		} `json:"tokens"`
	} `json:"color"`
	Type struct {
		Families map[string]string `json:"families"`
		Groups   []struct {
			Name   string `json:"name"`
			Family string `json:"family"`
			Styles []struct {
				Name       string `json:"name"`
				FontSize   string `json:"fontSize"`
				LineHeight string `json:"lineHeight"`
				FontWeight int    `json:"fontWeight"`
			} `json:"styles"`
		} `json:"groups"`
	} `json:"type"`
	Spacing struct {
		Tokens []struct{ Name, Value string } `json:"tokens"`
	} `json:"spacing"`
	Radius struct {
		Tokens []struct{ Name, Value string } `json:"tokens"`
	} `json:"radius"`
}

type nativeDesignSystem struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	Themes        []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"themes"`
	Colors map[string]struct {
		Value map[string]string `json:"value"`
		Usage string            `json:"usage"`
	} `json:"colors"`
	Typography map[string]struct {
		Size       int     `json:"size"`
		FontFamily string  `json:"font_family"`
		Weight     int     `json:"weight"`
		LineHeight float64 `json:"line_height"`
		Group      string  `json:"group"`
	} `json:"typography"`
	Spacing    map[string]int `json:"spacing"`
	Radii      map[string]int `json:"radii"`
	Components []struct {
		Name string `json:"name"`
	} `json:"components"`
}

func px(t *testing.T, value string) int {
	t.Helper()
	var n int
	if !regexp.MustCompile(`^[0-9]+px$`).MatchString(value) {
		t.Fatalf("%q is not a pixel value", value)
	}
	for _, c := range strings.TrimSuffix(value, "px") {
		n = n*10 + int(c-'0')
	}
	return n
}

// Every original token lands in the native file the Library view reads, and
// that file stays inside the rules components/features/artifacts/design-system/manifest.ts admits.
func TestMirrorDS216_EveryTokenIsInTheLibrary(t *testing.T) {
	t.Parallel()
	m := mirrorDS216Load(t)
	var original originalTokens
	if err := json.Unmarshal([]byte(m.file(t, "tokens.json")), &original); err != nil {
		t.Fatal(err)
	}
	source := m.file(t, "design-system.json")
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.DisallowUnknownFields()
	var native nativeDesignSystem
	if err := decoder.Decode(&native); err != nil {
		t.Fatalf("design-system.json: %v", err)
	}
	if native.SchemaVersion != 2 || native.Name != original.Name || len(source) > 32<<10 {
		t.Errorf("schema %d, name %q, %d bytes", native.SchemaVersion, native.Name, len(source))
	}
	name := regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
	family := regexp.MustCompile(`^[A-Za-z0-9 ,'"-]{1,120}$`)
	hex := regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	for group, n := range map[string]int{"colors": len(native.Colors), "typography": len(native.Typography), "spacing": len(native.Spacing), "radii": len(native.Radii)} {
		if n == 0 || n > 128 {
			t.Errorf("%s holds %d tokens", group, n)
		}
	}
	// Schema 2: each original token once, under its own name, with every theme's value and its usage note.
	if len(native.Themes) != len(original.Color.Themes) {
		t.Fatalf("themes %+v, original %+v", native.Themes, original.Color.Themes)
	}
	for i, theme := range original.Color.Themes {
		if native.Themes[i].ID != theme.ID || native.Themes[i].Name != theme.Name {
			t.Errorf("theme %d = %+v, want %+v", i, native.Themes[i], theme)
		}
	}
	for _, token := range original.Color.Tokens {
		got, ok := native.Colors[token.Name]
		if !ok || !name.MatchString(token.Name) || got.Usage != token.Usage || len(got.Value) != len(original.Color.Themes) {
			t.Errorf("color %s = %+v", token.Name, got)
			continue
		}
		for _, theme := range original.Color.Themes {
			if value := got.Value[theme.ID]; !hex.MatchString(value) || value != token.Value[theme.ID] {
				t.Errorf("color %s (%s) = %q, want %q", token.Name, theme.ID, value, token.Value[theme.ID])
			}
		}
	}
	if len(native.Colors) != len(original.Color.Tokens) {
		t.Errorf("%d native colors for %d original tokens", len(native.Colors), len(original.Color.Tokens))
	}
	styles := 0
	for _, group := range original.Type.Groups {
		for _, style := range group.Styles {
			styles++
			got, ok := native.Typography[style.Name]
			size, line := px(t, style.FontSize), px(t, style.LineHeight)
			if !ok || got.Size != size || got.Weight != style.FontWeight || got.FontFamily != original.Type.Families[group.Family] || !family.MatchString(got.FontFamily) || got.Group != group.Name {
				t.Errorf("type %s = %+v", style.Name, got)
			}
			// The Library holds a line height as a ratio; it must round back to the original pixels.
			if got.LineHeight < 0.5 || got.LineHeight > 4 || int(float64(size)*got.LineHeight+0.5) != line {
				t.Errorf("type %s line height %v × %dpx does not give %dpx", style.Name, got.LineHeight, size, line)
			}
		}
	}
	if len(native.Typography) != styles {
		t.Errorf("%d native text styles for %d original", len(native.Typography), styles)
	}
	for _, pair := range []struct {
		what     string
		original []struct{ Name, Value string }
		native   map[string]int
	}{{"spacing", original.Spacing.Tokens, native.Spacing}, {"radius", original.Radius.Tokens, native.Radii}} {
		if len(pair.original) != len(pair.native) {
			t.Errorf("%s: %d native for %d original", pair.what, len(pair.native), len(pair.original))
		}
		for _, token := range pair.original {
			if got, ok := pair.native[token.Name]; !ok || got != px(t, token.Value) {
				t.Errorf("%s %s = %d, want %s", pair.what, token.Name, got, token.Value)
			}
		}
	}
	if len(native.Components) != 1 || native.Components[0].Name != "Cover" || m.file(t, "components/Cover/preview.html") == "" {
		t.Errorf("components %+v", native.Components)
	}
	if !strings.HasPrefix(m.file(t, "README.md"), "# Template Crewship Design System\n") {
		t.Error("README.md is not the original brand book")
	}
}

// The real router accepts the manifest as an artifact project, and a second
// seed leaves the project as it is.
func TestMirrorDS216_SeedsThroughTheArtifactRoute(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	copy := testutil.MigratedDB(t)
	dbPath := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := database.Open("file:" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	db := opened.DB
	const workspace = "cmirrords216workspacexyz"
	token := "crewship_cli_" + strings.Repeat("d2", 16)
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('mirror-ds216-owner','mirror-ds216-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cmirrords216workspacexyz','Mirror','mirror-ds216')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('mirror-ds216-member','cmirrords216workspacexyz','mirror-ds216-owner','OWNER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('mirror-ds216-token','mirror-ds216-owner','fixture',?,'STANDARD','["pages:*"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{7}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "mirror-ds216-synthetic-router-secret", logger, api.WithJournal(audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := cli.NewClient(server.URL, token, workspace)
	item := mirrorDS216Item(t)

	for i, want := range []showcaseOutcome{showcaseCreated, showcaseExists} {
		var out bytes.Buffer
		if got := seedShowcaseArtifact(client, &out, item); got != want {
			t.Fatalf("run %d: outcome %v, want %v\n%s", i+1, got, want, out.String())
		}
	}
	resp, err := client.Get("/api/v1/pages/" + item.Slug + "/artifact")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snapshot struct {
		ArtifactKind  string `json:"artifact_kind"`
		LatestVersion int64  `json:"latest_version"`
		Manifest      struct {
			EntryPoint string            `json:"entry_point"`
			Files      []mirrorDS216File `json:"files"`
		} `json:"manifest"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&snapshot) != nil {
		t.Fatalf("snapshot: %s", resp.Status)
	}
	// An empty artifact_kind with design-system.json is what the Library gate opens (design-library.tsx isDesignLibrary).
	if snapshot.ArtifactKind != "" || snapshot.LatestVersion != 1 {
		t.Errorf("artifact_kind %q, latest version %d", snapshot.ArtifactKind, snapshot.LatestVersion)
	}
	sent := mirrorDS216Load(t)
	stored := map[string]mirrorDS216File{}
	for _, f := range snapshot.Manifest.Files {
		stored[f.Path] = f
	}
	if len(stored) != len(sent.Files) {
		t.Errorf("stored %d files, sent %d", len(stored), len(sent.Files))
	}
	for _, f := range sent.Files {
		if got := stored[f.Path]; got.Content != f.Content || got.MediaType != f.MediaType {
			t.Errorf("%s was not stored verbatim", f.Path)
		}
	}
}
