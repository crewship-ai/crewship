package managedlaunch

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestManagedDescriptorRejectsAmbiguousAndUnboundedEncoding(t *testing.T) {
	raw, err := json.Marshal(fuzzDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	good := string(raw)
	if _, err := DecodeDescriptor(base64.RawURLEncoding.EncodeToString(raw)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"case alias":        strings.Replace(good, `"path":`, `"Path":`, 1),
		"duplicate alias":   strings.Replace(good, `"path":`, `"Path":"/usr/bin/codex","path":`, 1),
		"escaped duplicate": strings.Replace(good, `"path":`, `"pa\u0074h":"/usr/bin/codex","path":`, 1),
		"trailing":          good + `{}`, "unknown": strings.Replace(good, `"path":`, `"unknown":1,"path":`, 1),
		"array": "[" + good + "]", "null": "null",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeDescriptor(base64.RawURLEncoding.EncodeToString([]byte(raw))); err == nil {
				t.Fatal("ambiguous descriptor accepted")
			}
		})
	}
	if _, err := DecodeDescriptor(strings.Repeat("A", (64<<10)+1)); err == nil {
		t.Fatal("unbounded descriptor accepted")
	}
}

func TestManagedProcessIdentity(t *testing.T) {
	// Fields3..22: state,ppid,pgrp,session,...,starttime. comm may contain ')'.
	fields := []string{"S", "1", "123", "123", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "17"}
	raw := "123 (native ) comm) " + strings.Join(fields, " ")
	if stamp, err := processStartIdentity([]byte(raw), 123); err != nil || stamp != 17 {
		t.Fatalf("positive identity: %d %v", stamp, err)
	}
	for name, index := range map[string]int{"foreign group": 2, "foreign session": 3, "zero start": 19} {
		t.Run(name, func(t *testing.T) {
			copyFields := append([]string(nil), fields...)
			copyFields[index] = "0"
			if _, err := processStartIdentity([]byte("123 (native) "+strings.Join(copyFields, " ")), 123); err == nil {
				t.Fatal("invalid identity admitted")
			}
		})
	}
	for _, raw := range []string{"broken", "123 (native) S 1", "123 (native) " + strings.Join(fields[:19], " ")} {
		if _, err := processStartIdentity([]byte(raw), 123); err == nil {
			t.Fatal("short identity admitted")
		}
	}
}
