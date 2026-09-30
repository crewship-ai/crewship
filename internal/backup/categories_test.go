package backup

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The console computes the contents preview itself while a plan is edited
// (backups-model.ts, CATEGORIES + resolveContents) and the server applies
// ContentCategories when it runs the plan. Parse the TypeScript constant and
// require the same keys, order, labels and dependencies.
func TestContentCategories_MatchTheConsole(t *testing.T) {
	src, err := os.ReadFile("../../components/features/admin/backups/backups-model.ts")
	if err != nil {
		t.Fatalf("read backups-model.ts: %v", err)
	}
	start := strings.Index(string(src), "export const CATEGORIES")
	if start < 0 {
		t.Fatal("backups-model.ts has no `export const CATEGORIES`")
	}
	block := string(src[start:])
	block = block[:strings.Index(block, "]\n")]
	row := regexp.MustCompile(`\{\s*key:\s*"(\w+)",\s*label:\s*"([^"]+)",\s*needs:\s*(?:null|"(\w+)")\s*\}`)
	var got []ContentCategory
	for _, m := range row.FindAllStringSubmatch(block, -1) {
		got = append(got, ContentCategory{Key: m[1], Label: m[2], Needs: m[3]})
	}
	if len(got) == 0 {
		t.Fatalf("parsed no rows from:\n%s", block)
	}
	if !reflect.DeepEqual(got, ContentCategories) {
		t.Fatalf("console CATEGORIES and ContentCategories differ:\nconsole: %+v\nserver:  %+v", got, ContentCategories)
	}
}

func TestTableCategory_CoversEveryBackupTable(t *testing.T) {
	for _, name := range BackupTables {
		if _, ok := TableCategory[name]; !ok {
			t.Errorf("BackupTables lists %q but TableCategory does not say which contents category it belongs to", name)
		}
	}
	listed := map[string]bool{}
	for _, name := range BackupTables {
		listed[name] = true
	}
	for name := range TableCategory {
		if !listed[name] {
			t.Errorf("TableCategory names %q, which BackupTables does not dump", name)
		}
	}
}

func TestResolveContents(t *testing.T) {
	all := []string{"agents", "memory", "chats", "att", "routines", "journal", "creds", "pages", "files"}
	cases := []struct {
		name     string
		preset   string
		contents []string
		envMode  string
		want     ContentsResolution
	}{
		{
			name: "complete with environments includes everything", preset: PresetComplete, envMode: EnvModeComplete,
			want: ContentsResolution{Included: append(append([]string{}, all...), "env"), Required: []RequiredCategory{}, Excluded: []string{}},
		},
		{
			name: "workspace preset without environments", preset: PresetWorkspace, envMode: EnvModeFiles,
			want: ContentsResolution{Included: all, Required: []RequiredCategory{}, Excluded: []string{"env"}},
		},
		{
			name: "memory only keeps agents as a dependency", preset: PresetCustom, contents: []string{"memory"}, envMode: EnvModeFiles,
			want: ContentsResolution{
				Included: []string{"memory"},
				Required: []RequiredCategory{{Key: "agents", Because: []string{"memory"}}},
				Excluded: []string{"chats", "att", "routines", "journal", "creds", "pages", "files", "env"},
			},
		},
		{
			name: "several dependants are all named", preset: PresetCustom, contents: []string{"memory", "routines", "att"}, envMode: EnvModeFiles,
			want: ContentsResolution{
				Included: []string{"memory", "att", "routines"},
				Required: []RequiredCategory{{Key: "agents", Because: []string{"memory", "routines"}}, {Key: "chats", Because: []string{"att"}}},
				Excluded: []string{"journal", "creds", "pages", "files", "env"},
			},
		},
		{
			name: "environments follow env_mode and pull in files", preset: PresetCustom, contents: []string{"env"}, envMode: EnvModeComplete,
			want: ContentsResolution{
				Included: []string{"env"},
				Required: []RequiredCategory{{Key: "files", Because: []string{"env"}}},
				Excluded: []string{"agents", "memory", "chats", "att", "routines", "journal", "creds", "pages"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveContents(tc.preset, tc.contents, tc.envMode)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
	if _, err := ResolveContents(PresetCustom, []string{"nope"}, EnvModeFiles); err == nil {
		t.Fatal("unknown category accepted")
	}
}

func TestCategoryFilter_MemoryOnly(t *testing.T) {
	f, err := newCategoryFilter([]string{CategoryAgents, CategoryMemory})
	if err != nil {
		t.Fatal(err)
	}
	d := &DBDump{Tables: map[string][]map[string]any{
		"workspaces": {{}}, "users": {{}}, "crews": {{}}, "agents": {{}}, "memory_versions": {{}},
		"chats": {{}}, "attachments": {{}}, "missions": {{}}, "journal_entries": {{}},
	}}
	dropped := f.filterDump(d)
	if want := []string{"attachments", "chats", "journal_entries", "missions"}; !reflect.DeepEqual(dropped, want) {
		t.Fatalf("dropped %v, want %v", dropped, want)
	}
	for _, keep := range []string{"workspaces", "users", "crews", "agents", "memory_versions"} {
		if _, ok := d.Tables[keep]; !ok {
			t.Errorf("memory-only dump lost %s", keep)
		}
	}
	if !f.section(SectionCrewMemory) || f.section(SectionCrewWorkspace) || f.section(SectionCrewHome) {
		t.Fatal("memory-only filter must carry the crew memory tree and no working files")
	}
	var full categoryFilter
	if !full.table("missions") || !full.section(SectionCrewHome) {
		t.Fatal("a nil filter is a full bundle")
	}
}
