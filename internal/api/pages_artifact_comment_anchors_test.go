package api

import (
	"encoding/json"
	"math"
	"testing"
)

func TestArtifactCommentAnchorStrictViewport(t *testing.T) {
	valid := []string{`{"schema_version":1,"type":"document"}`, `{"schema_version":1,"type":"point","coordinate_space":"viewport","x":0,"y":200,"viewport_width":100,"viewport_height":200}`, `{"schema_version":1,"type":"point","coordinate_space":"viewport","x":12.5,"y":25.25,"viewport_width":800,"viewport_height":600}`}
	for _, raw := range valid {
		var anchor artifactCommentAnchor
		if err := json.Unmarshal([]byte(raw), &anchor); err != nil {
			t.Fatalf("valid anchor rejected %s: %v", raw, err)
		}
	}
	invalid := []string{
		`null`, `{}`, `{"schema_version":2,"type":"document"}`, `{"schema_version":1,"type":"element","element_id":"x"}`, `{"schema_version":1,"type":"document","x":null}`, `{"schema_version":1,"type":"document","coordinate_space":""}`, `{"schema_version":1,"type":"document","x":0}`, `{"schema_version":1,"type":"document","type":"point"}`, `{"Schema_version":1,"type":"document"}`,
		`{"schema_version":1,"type":"point","coordinate_space":"document","x":10,"y":10,"viewport_width":100,"viewport_height":100}`,
		`{"schema_version":1,"type":"point","coordinate_space":"viewport","x":null,"y":10,"viewport_width":100,"viewport_height":100}`,
		`{"schema_version":1,"type":"point","coordinate_space":"viewport","x":10,"y":10,"viewport_width":0,"viewport_height":100}`,
		`{"schema_version":1,"type":"point","coordinate_space":"viewport","x":101,"y":10,"viewport_width":100,"viewport_height":100}`,
		`{"schema_version":1,"type":"point","coordinate_space":"viewport","x":10,"y":-1,"viewport_width":100,"viewport_height":100}`,
		`{"schema_version":1,"type":"point","coordinate_space":"viewport","x":10,"y":10,"viewport_width":20000,"viewport_height":100}`,
		`{"schema_version":1,"type":"point","coordinate_space":"viewport","x":1e300,"y":10,"viewport_width":100,"viewport_height":100}`,
		`{"schema_version":1,"type":"point","coordinate_space":"viewport","x":10,"y":10,"viewport_width":100,"viewport_height":100,"html":"unsafe"}`,
	}
	for _, raw := range invalid {
		var anchor artifactCommentAnchor
		if err := json.Unmarshal([]byte(raw), &anchor); err == nil {
			t.Fatalf("invalid anchor accepted %s", raw)
		}
	}
	space := "viewport"
	x, y, width, height := math.NaN(), 10.0, 100.0, 100.0
	if validateArtifactCommentAnchor(&artifactCommentAnchor{SchemaVersion: 1, Type: "point", CoordinateSpace: &space, X: &x, Y: &y, ViewportWidth: &width, ViewportHeight: &height}) == nil {
		t.Fatal("NaN accepted through direct caller")
	}
}
func TestArtifactPointAnchorRemainsBoundToExactRevision(t *testing.T) {
	h, _, ws, user, _ := discussionFixture(t)
	anchor := map[string]any{"schema_version": 1, "type": "point", "coordinate_space": "viewport", "x": 12.5, "y": 25.25, "viewport_width": 800, "viewport_height": 600}
	rr := artifactDiscussionCall(t, h, ws, user, "OWNER", "create", "", map[string]any{"revision": 1, "anchor": anchor, "body": "Viewport point only", "mutation_id": "point"})
	artifactExpect(t, rr, 201)
	thread := discussionThread(t, rr)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Next revision</h1>", "next", 1), 201)
	rr = artifactDiscussionCall(t, h, ws, user, "OWNER", "list", "", nil)
	artifactExpect(t, rr, 200)
	var result struct {
		Threads []artifactThreadWire `json:"threads"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Threads) != 1 || result.Threads[0].Revision != 1 || result.Threads[0].Anchor.Type != "point" || *result.Threads[0].Anchor.X != 12.5 {
		t.Fatal("point followed later head or lost coordinates")
	}
	artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"base_version": 1, "anchor": anchor, "body": "must not reanchor", "mutation_id": "change-anchor"}), 400)
}
