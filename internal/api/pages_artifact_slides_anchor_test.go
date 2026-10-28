package api

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func TestSlidesArtifactProjectionAnchorsMatchProjectValidator(t *testing.T) {
	source := `<section id="intro"><p id="title" style="left:0px;top:0px;width:1800px;height:200px;font-size:48px;color:#111111;text-align:left">` + strings.Repeat("😀", 501) + `</p><table id="sales" style="left:0px;top:300px;width:1800px;height:600px;font-size:24px;color:#111111;background-color:#FFFFFF;border-color:#111111"><tbody><tr><td>North</td><td>12</td></tr><tr><td>Žluťoučký&#160; kůň</td> <td>25</td></tr></tbody></table></section>`
	files := map[string]artifacts.VersionFile{
		"deck.json":         {Content: []byte(`{"schema_version":1,"order":["intro"]}`), MediaType: "application/json"},
		"slides/intro.html": {Content: []byte(source), MediaType: "text/html"},
	}
	projection, err := artifacts.ParseSlidesArtifact("page-id", files)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Anchors) != 2 {
		t.Fatal("missing authored anchors")
	}
	for id, anchor := range projection.Anchors {
		if len(anchor.Quote) > 2000 || !artifactProjectAnchor(files[anchor.Path].Content, anchor.Selector, anchor.Quote) {
			t.Fatalf("anchor %s incompatible with current Project authority: %+v", id, anchor)
		}
	}
	if projection.Anchors["sales"].Quote != "North12Žluťoučký kůň 25" {
		t.Fatalf("introduced table separators: %q", projection.Anchors["sales"].Quote)
	}
}
