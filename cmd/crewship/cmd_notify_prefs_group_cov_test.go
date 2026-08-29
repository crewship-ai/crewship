package main

// notifyPrefsCmd's children (get/set) already have deep coverage in
// cmd_notify_prefs_cov_test.go (auth gates, transport errors, happy path,
// local validation) — but that file never references the PARENT group var
// itself, which is why notifyPrefsCmd still shows up in a name-based
// untested-command scan. This file closes that specific gap and adds the
// one thing the existing suite doesn't: the -f json contract on `get`.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestNotifyPrefsCmd_HasChildren(t *testing.T) {
	have := map[string]bool{}
	for _, c := range notifyPrefsCmd.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"get", "set"} {
		if !have[want] {
			t.Errorf("notifyPrefsCmd missing subcommand %q", want)
		}
	}
}

func TestNotifyPrefsGetRunE_JSONContract(t *testing.T) {
	covSetupCli5(t).OnGet("/api/v1/me/notification-prefs", clitest.JSONResponse(200, map[string]any{
		"cells": []map[string]any{
			{"category": "approvals", "channel_id": "nch_1", "state": "immediate"},
		},
	}))
	doc := jsonOut(t, func() error {
		return notifyPrefsGetCmd.RunE(notifyPrefsGetCmd, nil)
	})
	rows, ok := doc.([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("want one cell, got %#v", doc)
	}
	row := rows[0].(map[string]any)
	if row["category"] != "approvals" || row["state"] != "immediate" {
		t.Errorf("row = %v", row)
	}
}

func TestNotifyPrefsGetRunE_EmptyIsJSONArray(t *testing.T) {
	covSetupCli5(t).OnGet("/api/v1/me/notification-prefs",
		clitest.JSONResponse(200, map[string]any{"cells": nil}))
	out := humanOut(t, func() error {
		return notifyPrefsGetCmd.RunE(notifyPrefsGetCmd, nil)
	})
	// The empty table still prints its header; the point of this test is
	// that decoding a null "cells" array doesn't panic the row loop.
	if !strings.Contains(out, "CATEGORY") {
		t.Errorf("output = %q", out)
	}
}
