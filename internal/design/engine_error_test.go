package design

import (
	"errors"
	"os/exec"
	"testing"
)

func TestRunErrorOnlyTreatsEngineRefusalAsInvalid(t *testing.T) {
	tests := []struct {
		name string
		cmd  []string
		want error
	}{
		{"refused update", []string{"sh", "-c", "exit 1"}, ErrInvalid},
		{"heap exhausted", []string{"sh", "-c", "exit 134"}, ErrUnavailable},
		{"killed", []string{"sh", "-c", "kill -9 $$"}, ErrUnavailable},
		{"could not start", []string{"/nonexistent/node"}, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exec.Command(tt.cmd[0], tt.cmd[1:]...).Run()
			if err == nil {
				t.Fatal("command succeeded")
			}
			if got := runError(err, "boom"); !errors.Is(got, tt.want) {
				t.Fatalf("runError = %v, want %v", got, tt.want)
			}
		})
	}
}
