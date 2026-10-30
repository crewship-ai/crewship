package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCraftHTTPErrorRedactsTokenAcrossBodyLimit(t *testing.T) {
	secret := strings.Repeat("ab12cd34", 8)
	for _, offset := range []int{4032, 4064, 4095, 4096} {
		body := strings.Repeat("x", offset) + secret + strings.Repeat("y", 80)
		err := craftHTTPError(&http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(body))}, secret)
		if err == nil {
			t.Fatal("conflict accepted")
		}
		// The token follows the x run directly, so any leaked prefix — even
		// a single byte cut by the body limit — shows up right after it.
		if strings.Contains(err.Error(), "x"+secret[:1]) {
			t.Fatalf("token prefix leaked at offset %d: %q", offset, err.Error()[max(0, len(err.Error())-40):])
		}
	}
}

func TestCraftHTTPErrorEarlierTokenDoesNotPullLookaheadIntoOutput(t *testing.T) {
	secret := strings.Repeat("ab12cd34", 8)
	body := secret + strings.Repeat("x", 4076) + secret
	err := craftHTTPError(&http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(body))}, secret)
	if err == nil || strings.Contains(err.Error(), secret[:8]) {
		t.Fatalf("token prefix exposed after earlier redaction: %v", err)
	}
}

func TestCraftHTTPErrorRedactsInterruptedTokenPrefix(t *testing.T) {
	secret := strings.Repeat("ab12cd34", 8)
	body := secret + " interrupted " + secret[:20]
	err := craftHTTPError(&http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(body))}, secret)
	if err == nil || strings.Contains(err.Error(), secret[:8]) {
		t.Fatalf("partial token exposed: %v", err)
	}
}
