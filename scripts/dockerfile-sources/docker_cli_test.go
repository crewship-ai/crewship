package dockerfilesources

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The runtime image needs the docker CLI (page builds, devcontainer images),
// but Alpine's docker-cli package is rebuilt on Alpine's schedule, not Go's:
// in October 2026 alpine:3.24 shipped it built with go1.26.8 and the image
// scan failed critical on its net/http advisories (#3047). The CLI therefore
// comes from Docker's static release, pinned by version and by a SHA-256 per
// architecture the image is published for.
func TestRuntimeDockerCLIIsPinnedStaticRelease(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(data)

	apkDockerCLI := regexp.MustCompile(`(?m)apk[^\n]*\badd\b[^\n]*\bdocker-cli\b`)
	if apkDockerCLI.MatchString(dockerfile) {
		t.Error("Dockerfile installs docker-cli from apk; Alpine's build lags Go security releases (#3047)")
	}

	version := regexp.MustCompile(`(?m)^ARG DOCKER_CLI_VERSION=(\d+\.\d+\.\d+)$`).FindStringSubmatch(dockerfile)
	if version == nil {
		t.Fatal("Dockerfile does not pin DOCKER_CLI_VERSION")
	}
	if !strings.Contains(dockerfile, "download.docker.com/linux/static/stable/") {
		t.Error("docker CLI is not fetched from Docker's static release")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		sum := regexp.MustCompile(`(?m)^ARG DOCKER_CLI_SHA256_` + arch + `=([0-9a-f]{64})$`)
		if !sum.MatchString(dockerfile) {
			t.Errorf("no SHA-256 pinned for the %s docker CLI archive", arch)
		}
	}
	if !strings.Contains(dockerfile, "sha256sum -c") {
		t.Error("the docker CLI archive is not checked against its pinned SHA-256")
	}
	if !regexp.MustCompile(`(?m)^COPY --from=dockercli \S+ /usr/local/bin/docker$`).MatchString(dockerfile) {
		t.Error("runtime stage does not copy the verified docker CLI")
	}
}
