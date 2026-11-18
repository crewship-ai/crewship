//go:build !windows

package crewstart

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestNativeToolExecEnvInspectFailureInheritsContainerPath(t *testing.T) {
	input := []string{"TERM=xterm-256color", "HOME=/home/agent"}
	for name, container := range map[string]provider.ContainerProvider{
		"unsupported": struct{ provider.ContainerProvider }{},
		"failed":      &pathProbeContainer{err: errors.New("inspect unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := NativeToolExecEnv(context.Background(), container, "fixture", input)
			if err == nil {
				t.Fatal("missing warning diagnostic")
			}
			if !reflect.DeepEqual(got, input) {
				t.Fatalf("failed metadata must leave PATH inherited; want TERM/HOME only: %v", got)
			}
		})
	}
}
