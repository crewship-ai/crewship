package main

import (
	"strings"
	"testing"
)

func TestHooksCmdStructure(t *testing.T) {
	t.Parallel()

	if hooksCmd.Use != "hooks" {
		t.Errorf("hooks Use: got %q want %q", hooksCmd.Use, "hooks")
	}
	if !strings.Contains(strings.ToLower(hooksCmd.Long), "not yet") &&
		!strings.Contains(strings.ToLower(hooksCmd.Long), "stub") {
		t.Errorf("hooks Long should document stub status; got %q", hooksCmd.Long)
	}
	have := map[string]bool{}
	for _, sub := range hooksCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "enable", "disable", "register"} {
		if !have[want] {
			t.Errorf("hooks missing subcommand %q; have %v", want, have)
		}
	}
}

func TestHooksSubcommandStubs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		run  func() error
	}{
		{"list", func() error { return hooksListCmd.RunE(hooksListCmd, nil) }},
		{"enable", func() error { return hooksEnableCmd.RunE(hooksEnableCmd, []string{"h-1"}) }},
		{"disable", func() error { return hooksDisableCmd.RunE(hooksDisableCmd, []string{"h-1"}) }},
		{"register", func() error { return hooksRegisterCmd.RunE(hooksRegisterCmd, nil) }},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.run()
			if err == nil || !strings.Contains(err.Error(), "not yet available") {
				t.Errorf("hooks %s: expected stub error; got %v", c.name, err)
			}
		})
	}
}

func TestHooksEnableArgsValidation(t *testing.T) {
	t.Parallel()

	if err := hooksEnableCmd.Args(hooksEnableCmd, []string{}); err == nil {
		t.Error("enable with no args should error")
	}
	if err := hooksDisableCmd.Args(hooksDisableCmd, []string{}); err == nil {
		t.Error("disable with no args should error")
	}
}
