package main

import "testing"

func TestReviewRegression_EmbeddedIdentifierIsNotInvocation(t *testing.T) {
	idx := &cliDocIndex{commands: map[string]bool{"saved-view update": true}}
	if idx.invokes("my-crewship saved-view update --shared", "saved-view update") {
		t.Fatal("embedded identifier accepted as CLI invocation")
	}
}
