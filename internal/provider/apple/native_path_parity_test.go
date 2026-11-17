package apple

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestBuildCreateArgsNativePathParity(t *testing.T) {
	for _, input := range []string{
		"/opt/mise/data/shims:/home/agent/.local/bin:/usr/local/share/npm-global/bin:/usr/bin:/bin",
		"/usr/local/share/npm-global/bin:/opt/mise/data/shims:/usr/bin:/bin",
	} {
		in := testCreateArgsInput()
		in.containerEnv = map[string]string{"PATH": input}
		args, err := buildCreateArgs(in)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		for i, arg := range args {
			if i > 0 && args[i-1] == "--env" && strings.HasPrefix(arg, "PATH=") {
				got = strings.TrimPrefix(arg, "PATH=")
			}
		}
		if want := devcontainer.NativeToolPath(input); got != want {
			t.Fatalf("Apple runtime PATH %q != shared policy %q", got, want)
		}
		if in.containerEnv["PATH"] != input {
			t.Fatal("mutated operator configuration")
		}
	}
}
