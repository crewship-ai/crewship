package apple

import (
	"context"
	"strings"
	"testing"
)

func TestDetectSuccessWithFakeCLI(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  system)
    if [ "$2" = "version" ]; then
      echo '[{"appName":"other","version":"0.1"},{"appName":"container","version":"9.9.9"}]'
    fi
    exit 0;;
esac
exit 0`)

	res, err := Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Version != "9.9.9" {
		t.Errorf("Version = %q, want 9.9.9", res.Version)
	}
	if res.HostIP == "" {
		t.Error("expected non-empty HostIP")
	}
}

func TestDetectCLINotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir, no container binary
	_, err := Detect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "apple container CLI not found") {
		t.Fatalf("err = %v, want CLI not found", err)
	}
}

func TestDetectVersionFails(t *testing.T) {
	installFakeContainer(t, `exit 1`)
	_, err := Detect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "apple container version") {
		t.Fatalf("err = %v, want version error", err)
	}
}

func TestDetectSystemNotRunning(t *testing.T) {
	installFakeContainer(t, `
case "$1 $2" in
  "system version") echo '[{"appName":"container","version":"1.0"}]'; exit 0;;
  "system status") echo 'apiserver is not running' >&2; exit 1;;
esac
exit 0`)

	_, err := Detect(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "apple container system not running") {
		t.Errorf("err = %v, want system not running", err)
	}
	if !strings.Contains(err.Error(), "apiserver is not running") {
		t.Errorf("err = %v, want stderr included", err)
	}
}

func TestGetVersionVariants(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "array without container appName falls back to first entry",
			body: `echo '[{"appName":"x","version":"5.5.5"}]'`,
			want: "5.5.5",
		},
		{
			name: "single json object",
			body: `echo '{"version":"7.7.7"}'`,
			want: "7.7.7",
		},
		{
			name: "non-json output returned trimmed",
			body: `echo '  plain-1.0  '`,
			want: "plain-1.0",
		},
		{
			name: "json format fails, plain fallback succeeds",
			body: `
if [ "$#" -eq 4 ]; then exit 1; fi
echo '  2.0.0  '`,
			want: "2.0.0",
		},
		{
			name: "array entry matching container appName wins",
			body: `echo '[{"appName":"container","version":"3.3.3"},{"appName":"x","version":"0.0.1"}]'`,
			want: "3.3.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeContainer(t, tt.body)
			got, err := getVersion(context.Background())
			if err != nil {
				t.Fatalf("getVersion: %v", err)
			}
			if got != tt.want {
				t.Errorf("getVersion = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetVersionBothCommandsFail(t *testing.T) {
	installFakeContainer(t, `exit 1`)
	_, err := getVersion(context.Background())
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err = %v, want version error", err)
	}
}

func TestCheckSystemStatusOK(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	if err := checkSystemStatus(context.Background()); err != nil {
		t.Fatalf("checkSystemStatus: %v", err)
	}
}
