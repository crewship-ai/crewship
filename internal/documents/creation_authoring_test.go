package documents

import (
	"strings"
	"testing"
)

func TestCreationAuthoringLimitsAndUnicodeBlankSet(t *testing.T) {
	paragraph := func(text string) AuthoringBlock { return AuthoringBlock{Type: "paragraph", Text: &text} }
	for _, text := range []string{"\ufeff", "\u0085", " \t\r\n\u00a0\u1680\u2000\u200a\u2028\u2029\u202f\u205f\u3000"} {
		if ValidateAuthoring([]AuthoringBlock{paragraph(text)}) == nil {
			t.Fatalf("accepted blank %q", text)
		}
	}
	for _, text := range []string{"x\ufeff", "\u0085x", "český text"} {
		if err := ValidateAuthoring([]AuthoringBlock{paragraph(text)}); err != nil {
			t.Fatal(err)
		}
	}
	blocks := make([]AuthoringBlock, 4)
	for i := range blocks {
		blocks[i] = paragraph(strings.Repeat("é", CreationMaxItemBytes/2))
	}
	if err := ValidateAuthoring(blocks); err != nil {
		t.Fatal(err)
	}
	if ValidateAuthoring(append(blocks, paragraph("x"))) == nil {
		t.Fatal("total text overflow accepted")
	}
	if ValidateAuthoring([]AuthoringBlock{paragraph(strings.Repeat("x", CreationMaxItemBytes+1))}) == nil {
		t.Fatal("item overflow accepted")
	}
	list := AuthoringBlock{Type: "bulletList", Items: make([]string, 128)}
	for i := range list.Items {
		list.Items[i] = "item"
	}
	if ValidateAuthoring([]AuthoringBlock{list, list, list}) == nil {
		t.Fatal("generated block overflow accepted")
	}
}
