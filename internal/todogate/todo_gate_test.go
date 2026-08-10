package todogate

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	markerWords = "TO" + "DO|FIX" + "ME"
	debtMarker  = regexp.MustCompile(`\b(?:` + markerWords + `)(?:\([^)]*\))?:`)
	issueMarker = regexp.MustCompile(`\b(?:` + markerWords + `)\(#[0-9]+\):`)
)

func TestGoDebtCommentsReferenceIssues(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, root := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}

			files := token.NewFileSet()
			parsed, err := parser.ParseFile(files, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			for _, group := range parsed.Comments {
				for _, comment := range group.List {
					if !debtMarker.MatchString(comment.Text) || issueMarker.MatchString(comment.Text) {
						continue
					}
					position := files.Position(comment.Pos())
					rel, relErr := filepath.Rel(repoRoot, position.Filename)
					if relErr != nil {
						return relErr
					}
					t.Errorf("%s:%d: untriaged debt marker %q; remove it or add a numeric GitHub issue reference", filepath.ToSlash(rel), position.Line, strings.TrimSpace(comment.Text))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
