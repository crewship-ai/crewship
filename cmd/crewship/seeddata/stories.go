package seeddata

import (
	"embed"
	"io/fs"
)

// StoryFiles are the small, local business fixtures behind the default demo.
// They are separate from the external-source watch packs: a story can start
// only when a person presses Run, including when it asks for approval.
var StoryFiles = []struct {
	CrewSlug string
	Source   string
	Dest     string
}{
	{"ops", "stories/harbor-goods/leads.json", "shared/demo/harbor-goods/leads.json"},
	{"ops", "stories/harbor-goods/check_leads.py", "shared/scripts/harbor-goods/check_leads.py"},
}

//go:embed stories/harbor-goods/leads.json stories/harbor-goods/check_leads.py
var storiesFS embed.FS

func StoryFileContent(source string) ([]byte, error) { return fs.ReadFile(storiesFS, source) }
