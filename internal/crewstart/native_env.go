package crewstart

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

// NativeToolExecEnv repairs only PATH from read-only runtime metadata. It
// never starts a probe process or copies other container/run environment.
// On inspection failure it omits PATH and returns a diagnostic. Docker/Apple
// exec then inherit the complete container environment, including feature paths.
func NativeToolExecEnv(ctx context.Context, container provider.ContainerProvider, id string, env []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var path string
	var err error
	if inspector, ok := container.(provider.ContainerPathProvider); ok {
		path, err = inspector.ContainerPath(ctx, id)
	} else {
		err = fmt.Errorf("container PATH inspection is unsupported")
	}
	if err != nil {
		path = ""
	}
	if len(path) > 32768 || strings.ContainsAny(path, "\x00\r\n") {
		path = ""
		err = fmt.Errorf("container PATH metadata is invalid")
	}
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, "PATH=") {
			out = append(out, item)
		}
	}
	if err != nil {
		return out, err
	}
	return append(out, "PATH="+devcontainer.NativeToolPath(path)), nil
}
