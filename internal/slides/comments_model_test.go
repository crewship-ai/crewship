package slides

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestCommentStrictImmutableAnchorAdmission(t *testing.T) {
	good := `{"page_id":"page_original","schema_version":1,"base_rev":7,"mutation_id":"thread_original","anchor":{"anchor_schema_version":1,"kind":"element","slide_id":"slide_one","element_id":"element_one","revision":7},"body":"Příliš žluťoučký kůň 🐎\nKomentář"}`
	request, err := DecodeCreateComment([]byte(good))
	if err != nil || request.PageID != "page_original" || request.Anchor.AnchorSchemaVersion != 1 || request.Anchor.Revision != 7 {
		t.Fatalf("valid anchor %#v %v", request, err)
	}
	for name, bad := range map[string]string{
		"case alias PageID":      strings.Replace(good, `"page_id":"page_original"`, `"page_id":"page_original","PAGE_ID":"other_page"`, 1),
		"case alias anchor":      strings.Replace(good, `"revision":7`, `"revision":7,"REVISION":8`, 1),
		"unknown anchor field":   strings.Replace(good, `"revision":7`, `"revision":7,"selector":"similar"`, 1),
		"missing anchor version": strings.Replace(good, `"anchor_schema_version":1,`, "", 1),
		"future protocol":        strings.Replace(good, `"schema_version":1`, `"schema_version":2`, 1),
		"future anchor version":  strings.Replace(good, `"anchor_schema_version":1`, `"anchor_schema_version":2`, 1),
		"anchor version null":    strings.Replace(good, `"anchor_schema_version":1`, `"anchor_schema_version":null`, 1),
		"wrong original rev":     strings.Replace(good, `"revision":7`, `"revision":8`, 1),
		"null target":            strings.Replace(good, `"element_id":"element_one"`, `"element_id":null`, 1),
		"unknown kind":           strings.Replace(good, `"kind":"element"`, `"kind":"quote"`, 1),
		"blank body":             strings.Replace(good, `"body":"Příliš žluťoučký kůň 🐎\nKomentář"`, `"body":" \n\t"`, 1),
		"duplicate body":         strings.Replace(good, `"body":`, `"body":"other","body":`, 1),
		"trailing JSON":          good + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCreateComment([]byte(bad)); err == nil {
				t.Fatalf("accepted %s", bad)
			}
		})
	}
	slide := strings.Replace(strings.Replace(good, `"kind":"element"`, `"kind":"slide"`, 1), `"element_id":"element_one",`, "", 1)
	if _, err := DecodeCreateComment([]byte(slide)); err != nil {
		t.Fatal("valid whole-slide anchor", err)
	}
	if _, err := DecodeCreateComment([]byte(strings.Replace(good, `"kind":"element"`, `"kind":"slide"`, 1))); err == nil {
		t.Fatal("whole slide accepted unrelated element target")
	}
	var oversized map[string]any
	if json.Unmarshal([]byte(good), &oversized) != nil {
		t.Fatal("fixture")
	}
	oversized["body"] = strings.Repeat("č", MaxCommentBodyBytes/2+1)
	raw, _ := json.Marshal(oversized)
	if _, err := DecodeCreateComment(raw); err == nil {
		t.Fatal("accepted oversized UTF8 body")
	}
}
func TestCommentResolveRequiresActualBooleanAndVersion(t *testing.T) {
	good := `{"page_id":"page_original","schema_version":1,"mutation_id":"resolve_one","if_version":1,"resolved":false}`
	if _, err := DecodeResolveComment([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"null", `"false"`, `0`, `[]`, `{}`} {
		if _, err := DecodeResolveComment([]byte(strings.Replace(good, "false", value, 1))); err == nil {
			t.Fatalf("accepted resolved%s", value)
		}
	}
	if _, err := DecodeResolveComment([]byte(strings.Replace(good, `"if_version":1`, `"if_version":0`, 1))); err == nil {
		t.Fatal("accepted missing version")
	}
	reply := `{"page_id":"page_original","schema_version":1,"mutation_id":"reply_one","if_version":1,"body":"Reply"}`
	if _, err := DecodeReplyComment([]byte(reply)); err != nil {
		t.Fatal(err)
	}
}

func TestCommentCursorStrictBoundaries(t *testing.T) {
	key := Key{WorkspaceID: "workspace", PageID: "page"}
	valid := `{"cursor_schema_version":1,"workspace_id":"workspace","page_id":"page","after":1,"watermark":2}`
	raws := []string{
		`null`, `[]`, `{}`, valid + `{}`, strings.Replace(valid, `"after":1`, `"after":null`, 1),
		strings.Replace(valid, `"after":1`, `"AFTER":1`, 1), strings.Replace(valid, `"after":1`, `"after":1,"after":2`, 1),
		strings.Replace(valid, `"after":1`, `"after":-1`, 1), strings.Replace(valid, `"watermark":2`, `"watermark":0`, 1),
		strings.Replace(valid, `"page"`, `"foreign"`, 1), strings.Replace(valid, `"cursor_schema_version":1`, `"cursor_schema_version":2`, 1),
		strings.Replace(valid, `"after":1`, `"after":1,"thread_id":"foreign"`, 1),
	}
	for _, raw := range raws {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(raw))
		if _, _, _, err := decodeCommentCursor(key, "", CommentPageRequest{Cursor: cursor}); err == nil {
			t.Fatalf("accepted cursor %s", raw)
		}
	}
	for _, cursor := range []string{"%", strings.Repeat("A", 1025)} {
		if _, _, _, err := decodeCommentCursor(key, "", CommentPageRequest{Cursor: cursor}); err == nil {
			t.Fatal("accepted malformed cursor")
		}
	}
	if _, _, _, err := decodeCommentCursor(key, "", CommentPageRequest{Cursor: base64.RawURLEncoding.EncodeToString([]byte(valid))}); err != nil {
		t.Fatal(err)
	}
}

func TestCommentProtocolIndependentOfDeckVersions(t *testing.T) {
	for _, raw := range []string{
		`{"page_id":"page","schema_version":2,"base_rev":1,"mutation_id":"create","anchor":{"anchor_schema_version":1,"kind":"slide","slide_id":"slide","revision":1},"body":"Comment"}`,
		`{"page_id":"page","schema_version":2,"mutation_id":"reply","if_version":1,"body":"Reply"}`,
		`{"page_id":"page","schema_version":2,"mutation_id":"resolve","if_version":1,"resolved":true}`,
	} {
		if strings.Contains(raw, `"base_rev"`) {
			if _, err := DecodeCreateComment([]byte(raw)); err == nil {
				t.Fatal("future create protocol accepted")
			}
		} else if strings.Contains(raw, `"body"`) {
			if _, err := DecodeReplyComment([]byte(raw)); err == nil {
				t.Fatal("future reply protocol accepted")
			}
		} else {
			if _, err := DecodeResolveComment([]byte(raw)); err == nil {
				t.Fatal("future resolve protocol accepted")
			}
		}
	}
}
