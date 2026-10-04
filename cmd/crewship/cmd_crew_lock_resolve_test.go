package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolverConfigInputValidation(t *testing.T) {
	for _, tc := range []struct {
		input string
		ok    bool
	}{{`{"tools":{"node":"22"}}`, true}, {"[tools]\nnode = \"22.0.0\"", true}, {`{"tools":{}}`, false}, {`{"tools":{"node":"22;echo secret"}}`, false}, {`{"tools":{"node":"22"},"lock":{"schema_version":1,"files":{"../mise.lock":"x"}}}`, false}} {
		t.Run(tc.input, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "mise.json")
			if err := os.WriteFile(p, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := readResolverConfig(p)
			if (err == nil) != tc.ok {
				t.Fatalf("valid=%v err=%v", tc.ok, err)
			}
		})
	}
}
