package pagesearch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/slides"
)

func documentFixture(t *testing.T) (SourceTag, []DocumentBlock, []BlockHash) {
	t.Helper()
	blocks := []BlockHash{{"list", strings.Repeat("a", 64)}, {"item", strings.Repeat("b", 64)}, {"leaf", strings.Repeat("c", 64)}, {"heading", strings.Repeat("d", 64)}}
	hash, err := DocumentHash(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return SourceTag{"workspace", "page", "document", 2, 5, 3, hash}, []DocumentBlock{
		{"list", "bulletList", "", blocks[0].Hash, "needle duplicated ancestor"},
		{"item", "listItem", "list", blocks[1].Hash, "needle duplicated ancestor"},
		{"leaf", "paragraph", "item", blocks[2].Hash, "needle actual leaf"},
		{"heading", "heading", "", blocks[3].Hash, "second term"},
	}, blocks
}

func TestDocumentLeafProjectionAndCompleteSourceBinding(t *testing.T) {
	source, outline, blocks := documentFixture(t)
	projection, err := FromDocument(source, outline, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Segments) != 2 || projection.Segments[0].Text != "needle actual leaf" {
		t.Fatalf("ancestor duplication: %+v", projection)
	}
	if _, _, err = projection.Encode(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SourceTag, *[]DocumentBlock, *[]BlockHash){
		"stale source":     func(s *SourceTag, _ *[]DocumentBlock, _ *[]BlockHash) { s.Hash = strings.Repeat("e", 64) },
		"mismatched leaf":  func(_ *SourceTag, o *[]DocumentBlock, _ *[]BlockHash) { (*o)[2].Hash = strings.Repeat("e", 64) },
		"missing ancestor": func(_ *SourceTag, o *[]DocumentBlock, _ *[]BlockHash) { *o = (*o)[1:] },
		"cycle":            func(_ *SourceTag, o *[]DocumentBlock, _ *[]BlockHash) { (*o)[0].Parent = "item" },
		"unknown schema":   func(s *SourceTag, _ *[]DocumentBlock, _ *[]BlockHash) { s.Schema = 4 },
		"unknown type":     func(_ *SourceTag, o *[]DocumentBlock, _ *[]BlockHash) { (*o)[2].Type = "codeBlock" },
	} {
		t.Run(name, func(t *testing.T) {
			s, o, b := documentFixture(t)
			mutate(&s, &o, &b)
			if _, err := FromDocument(s, o, b); err == nil {
				t.Fatal("invalid source admitted")
			}
		})
	}
	// Reordering the persisted complete set cannot alter its source hash.
	blocks[0], blocks[3] = blocks[3], blocks[0]
	hash, _ := DocumentHash(blocks)
	if hash != source.Hash {
		t.Fatal("persisted order changed source identity")
	}
}

func TestSlidesNativeProjectionPreservesVersionHashAndIgnoresImages(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/slides-schema-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Deck json.RawMessage `json:"deck"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	deck, err := slides.Decode(fixture.Deck)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			candidate := deck
			candidate.Slides = append([]slides.Slide{}, deck.Slides...)
			candidate.Slides[0].Elements = append([]slides.Element{}, deck.Slides[0].Elements...)
			candidate.SchemaVersion = version
			candidate.Slides[0].Notes = "searchable presenter notes"
			if version == 2 {
				candidate.Slides[0].Elements = append(candidate.Slides[0].Elements, slides.Element{ID: "image", Type: "image", X: 0, Y: 0, Width: 10, Height: 10, Image: &slides.Image{AssetID: "asset", ExpectedSHA256: strings.Repeat("a", 64), Alt: "not indexed image canary"}})
			}
			canonical, err := slides.Canonical(candidate)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := slides.DeckHash(candidate)
			if err != nil {
				t.Fatal(err)
			}
			source := SourceTag{"workspace", candidate.DeckID, "slides", version, 7, 0, hash}
			projection, err := FromSlides(source, canonical)
			if err != nil {
				t.Fatal(err)
			}
			for _, segment := range projection.Segments {
				if strings.Contains(segment.Text, "canary") {
					t.Fatal("image metadata was indexed")
				}
			}
			q, _ := ParseQuery("presenter notes")
			if _, matched, err := q.Match(projection); err != nil || !matched {
				t.Fatalf("notes not searchable: %v", err)
			}
			source.Hash = strings.Repeat("f", 64)
			if _, err = FromSlides(source, canonical); err == nil {
				t.Fatal("wrong revision hash admitted")
			}
		})
	}
}

func TestSourceFencesBudgetsAndUnicodePageMatching(t *testing.T) {
	source, outline, blocks := documentFixture(t)
	projection, err := FromDocument(source, outline, blocks)
	if err != nil {
		t.Fatal(err)
	}
	current := source
	if !source.Current(current) {
		t.Fatal("identical source refused")
	}
	current.Generation++
	if source.Current(current) {
		t.Fatal("restore generation ignored")
	}
	current = source
	current.Revision++
	if source.Current(current) {
		t.Fatal("revision ignored")
	}
	current = source
	current.PageID = "different"
	if source.Current(current) {
		t.Fatal("identity ignored")
	}
	projection.Segments[0].Text = strings.Repeat("č", 170) + " Straße first " + strings.Repeat("ě", 400)
	projection.Segments[1].Text = "SECOND elsewhere"
	q, err := ParseQuery("STRASSE second")
	if err != nil {
		t.Fatal(err)
	}
	text, matched, err := q.Match(projection)
	if err != nil || !matched || !strings.Contains(text, "Straße") || !utf8.ValidString(text) || len(text) > MaxSnippetBytes {
		t.Fatalf("bad bounded Unicode snippet %q %v", text, err)
	}
	absent, _ := ParseQuery("strasse absent")
	if _, matched, _ = absent.Match(projection); matched {
		t.Fatal("not all terms required")
	}
	projection.Segments[0].Text = strings.Repeat("x", MaxTextBytes+1)
	if err = projection.Validate(); !errors.Is(err, ErrBudget) {
		t.Fatalf("unbounded projection: %v", err)
	}
	for _, raw := range []string{"", strings.Repeat("x", MaxQueryBytes+1), "bad\x00query"} {
		if _, err = ParseQuery(raw); err == nil {
			t.Fatal("bad query admitted")
		}
	}
}

func TestProjectionCacheEncodingRejectsCorruption(t *testing.T) {
	source, outline, blocks := documentFixture(t)
	projection, err := FromDocument(source, outline, blocks)
	if err != nil {
		t.Fatal(err)
	}
	raw, hash, err := projection.Encode()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Decode(raw, hash)
	if err != nil || !restored.Source.Current(source) {
		t.Fatalf("valid cache rejected: %v", err)
	}
	bad := append([]byte{}, raw...)
	bad[len(bad)-2] ^= 1
	if _, err = Decode(bad, hash); err == nil {
		t.Fatal("corrupt bytes admitted")
	}
	for _, raw := range [][]byte{
		[]byte(strings.Replace(string(raw), `"adapter_version":2`, `"adapter_version":2,"adapter_version":2`, 1)),
		[]byte(strings.Replace(string(raw), `"adapter_version":2`, `"adapter_version":1`, 1)),
		[]byte(strings.Replace(string(raw), `"adapter_version":2`, `"adapter_version":2,"unexpected":true`, 1)),
		append(append([]byte{}, raw...), []byte(` {}`)...),
	} {
		sum := sha256.Sum256(raw)
		if _, err = Decode(raw, hex.EncodeToString(sum[:])); err == nil {
			t.Fatal("invalid cache encoding admitted")
		}
	}
	if _, err = Decode(make([]byte, MaxProjectionBytes+1), hash); !errors.Is(err, ErrBudget) {
		t.Fatal("cache byte budget not enforced before parse")
	}
}

func TestFreshDocumentRevisionZeroIsSearchable(t *testing.T) {
	source, outline, blocks := documentFixture(t)
	source.Revision = 0
	projection, err := FromDocument(source, outline, blocks)
	if err != nil || !projection.Source.Current(source) {
		t.Fatalf("initialized Doc head0 refused: %v", err)
	}
	query, _ := ParseQuery("needle second")
	if _, matched, err := query.Match(projection); err != nil || !matched {
		t.Fatalf("Doc head0 not searchable: %v", err)
	}
	source.Revision = -1
	if source.Validate() == nil || projection.Source.Current(source) {
		t.Fatal("negative revision accepted")
	}
	source.Type, source.Schema, source.Generation, source.Revision = "slides", 1, 0, 0
	if source.Validate() == nil {
		t.Fatal("Slides head0 accepted")
	}
	source.Revision = 1
	if err := source.Validate(); err != nil {
		t.Fatalf("Slides head1 rejected: %v", err)
	}
}

func TestDocumentAdapterMatchedVersionsAndRichCodeLeaf(t *testing.T) {
	source, outline, blocks := documentFixture(t)
	for _, schema := range []int{1, 2, 3} {
		source.Schema = schema
		projection, err := FromDocument(source, outline, blocks)
		if err != nil || len(projection.Segments) != 2 {
			t.Fatalf("schema%d leaf adapter: %+v %v", schema, projection, err)
		}
	}
	source.Schema = 3
	outline[len(outline)-1].Type = "codeBlock"
	projection, err := FromDocument(source, outline, blocks)
	if err != nil || projection.Segments[1].Kind != "codeBlock" {
		t.Fatalf("rich code leaf unavailable: %+v %v", projection, err)
	}
	for _, schema := range []int{1, 2, 4} {
		source.Schema = schema
		if _, err := FromDocument(source, outline, blocks); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("schema%d admitted unsupported code leaf: %v", schema, err)
		}
	}
}
