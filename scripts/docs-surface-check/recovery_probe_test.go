package main

import (
	"strings"
	"testing"
)

func TestReviewRegression_IndentedHeading(t *testing.T) {
	for indent := 0; indent <= 3; indent++ {
		root := t.TempDir()
		writeDocsPage(t, root, "docs/probe.mdx", strings.Repeat(" ", indent)+"## GET /things/{id}\n")
		got, err := unescapedHeadingExpressions(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("indent %d escaped detection: %v", indent, got)
		}
	}
}
func TestReviewRegression_NestedSuggestedFix(t *testing.T) {
	text := "## {{ secrets.kind }}"
	expr := unescapedExpression(text)
	fixed := strings.Replace(text, expr, escapeExpression(expr), 1)
	if got := unescapedExpression(fixed); got != "" {
		t.Fatalf("suggested fix still contains expression: %q", got)
	}
}
