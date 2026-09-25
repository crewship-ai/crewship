package seeddata

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
)

type StoryDef struct {
	Slug         string                   `json:"slug" yaml:"slug"`
	Project      string                   `json:"project" yaml:"project"`
	Name         string                   `json:"name" yaml:"name"`
	Crew         string                   `json:"crew" yaml:"crew"`
	Agent        string                   `json:"agent" yaml:"agent"`
	Problem      string                   `json:"problem" yaml:"problem"`
	CheckLabel   string                   `json:"check_label" yaml:"check_label"`
	ResolveLabel string                   `json:"resolve_label" yaml:"resolve_label"`
	IssueTitle   string                   `json:"issue_title" yaml:"issue_title"`
	Source       string                   `json:"source" yaml:"source"`
	Result       string                   `json:"result" yaml:"result"`
	SampleDraft  string                   `json:"sample_draft" yaml:"sample_draft"`
	Approval     bool                     `json:"approval" yaml:"approval"`
	Rows         []map[string]interface{} `json:"rows" yaml:"rows"`
}

//go:embed stories/business/*
var storiesFS embed.FS

var Stories = loadStories()

func loadStories() []StoryDef {
	b, err := storiesFS.ReadFile("stories/business/catalogue.json")
	if err != nil {
		panic(err)
	}
	var out []StoryDef
	if err = json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	seen := map[string]bool{}
	for _, s := range out {
		if s.Slug == "" || seen[s.Slug] || s.Crew == "" || s.Agent == "" {
			panic(fmt.Sprintf("invalid demo story %q", s.Slug))
		}
		seen[s.Slug] = true
	}
	return out
}

type StoryFile struct{ CrewSlug, Source, Dest string }

var StoryFiles = storyFiles()

func storyFiles() []StoryFile {
	var out []StoryFile
	for _, crew := range []string{"engineering", "quality", "ops"} {
		for _, file := range []string{"story.py", "catalogue.json", "connector.py", "live.py"} {
			out = append(out, StoryFile{crew, "stories/business/" + file, "shared/demo/business/" + file})
		}
	}
	return out
}
func StoryFileContent(source string) ([]byte, error) { return fs.ReadFile(storiesFS, source) }
