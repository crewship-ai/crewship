// Package containerstate captures the *actual* contents of a crew's
// container so the journal can answer "what's actually installed in
// here?" — independent of whatever devcontainer.json declared.
//
// devcontainer.json is the user's intent; this package records what the
// agents (lead, members, and any postCreateCommand they ran) actually
// produced. Each Snapshot turns into a single container.snapshot journal
// entry that lists apt packages, pip packages, and npm globals; the
// caller dedups consecutive identical snapshots so the journal is a real
// delta log, not a heartbeat stream.
//
// Soft-fail by design: any package manager that isn't present in the
// container (no dpkg on alpine, no pip on a pure-go image, …) just
// produces an empty list. Failures are logged at debug, never propagated
// — a snapshot that captures three out of four sources is strictly more
// useful than no snapshot at all.
package containerstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Snapshot is the structured result of probing a crew container for its
// actual installed package set. JSON-friendly so it serialises cleanly
// into a journal entry payload.
type Snapshot struct {
	APT  []Package `json:"apt,omitempty"`
	Pip  []Package `json:"pip,omitempty"`
	Npm  []Package `json:"npm,omitempty"`
	OS   string    `json:"os,omitempty"` // e.g. "Ubuntu 24.04" — pulled from /etc/os-release for context
	Errs []string  `json:"errs,omitempty"`
}

// Package is a single name+version pair. Versions are reported verbatim
// from the package manager so the consumer can spot pinned vs. floating
// installs without us having to re-parse semver.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Capture probes the container with three short execs and returns a
// Snapshot. Any single probe failing only contributes to Snapshot.Errs;
// the function itself only returns an error when no probes succeeded
// (typically because the container isn't running).
func Capture(ctx context.Context, c provider.ContainerProvider, containerID string) (Snapshot, error) {
	var snap Snapshot
	successes := 0

	if pkgs, err := captureAPT(ctx, c, containerID); err == nil {
		snap.APT = pkgs
		successes++
	} else {
		snap.Errs = append(snap.Errs, "apt: "+err.Error())
	}

	if pkgs, err := capturePip(ctx, c, containerID); err == nil {
		snap.Pip = pkgs
		successes++
	} else {
		snap.Errs = append(snap.Errs, "pip: "+err.Error())
	}

	if pkgs, err := captureNpm(ctx, c, containerID); err == nil {
		snap.Npm = pkgs
		successes++
	} else {
		snap.Errs = append(snap.Errs, "npm: "+err.Error())
	}

	if osRelease, err := captureOS(ctx, c, containerID); err == nil {
		snap.OS = osRelease
		successes++
	} else {
		snap.Errs = append(snap.Errs, "os: "+err.Error())
	}

	if successes == 0 {
		return Snapshot{}, fmt.Errorf("containerstate: no probes succeeded against %s", containerID)
	}
	return snap, nil
}

// Hash returns a stable digest of the snapshot. Equal hashes mean equal
// contents — callers use it to skip emitting a journal entry when a
// previous snapshot already recorded the same state.
func (s Snapshot) Hash() string {
	h := sha256.New()
	writeList(h, "apt", s.APT)
	writeList(h, "pip", s.Pip)
	writeList(h, "npm", s.Npm)
	_, _ = h.Write([]byte("os:" + s.OS))
	return hex.EncodeToString(h.Sum(nil))
}

func writeList(w io.Writer, label string, pkgs []Package) {
	_, _ = fmt.Fprintf(w, "%s\n", label)
	// Snapshots are already sorted by Capture; re-sort defensively so a
	// caller that handed in an unsorted slice can't change the hash.
	sorted := append([]Package(nil), pkgs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, p := range sorted {
		_, _ = fmt.Fprintf(w, "%s=%s\n", p.Name, p.Version)
	}
}

// captureAPT lists apt-installed packages via dpkg-query. Returns
// ErrProbeNotApplicable when dpkg isn't available (alpine, distroless,
// scratch base images) — the caller treats that as "no apt packages".
func captureAPT(ctx context.Context, c provider.ContainerProvider, containerID string) ([]Package, error) {
	out, err := exec(ctx, c, containerID,
		"sh", "-c",
		"command -v dpkg-query >/dev/null 2>&1 && dpkg-query -W -f='${Package}\t${Version}\n' 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var pkgs []Package
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if parts[0] == "" {
			continue
		}
		p := Package{Name: parts[0]}
		if len(parts) == 2 {
			p.Version = parts[1]
		}
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	return pkgs, nil
}

// capturePip lists Python packages via pip freeze. Falls back to pip3
// because some base images only ship pip3 on PATH. The pkg==ver shape
// pip emits is parsed leniently.
func capturePip(ctx context.Context, c provider.ContainerProvider, containerID string) ([]Package, error) {
	out, err := exec(ctx, c, containerID,
		"sh", "-c",
		`if command -v pip >/dev/null 2>&1; then pip freeze 2>/dev/null; elif command -v pip3 >/dev/null 2>&1; then pip3 freeze 2>/dev/null; else true; fi`)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var pkgs []Package
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// pip freeze can emit `-e git+…#egg=name` lines for editable
		// installs. Use the egg= name as the package name and leave
		// the version empty so the snapshot still records its presence.
		if strings.HasPrefix(line, "-e ") {
			if idx := strings.Index(line, "#egg="); idx >= 0 {
				pkgs = append(pkgs, Package{Name: strings.TrimSpace(line[idx+5:])})
			}
			continue
		}
		parts := strings.SplitN(line, "==", 2)
		if parts[0] == "" {
			continue
		}
		p := Package{Name: parts[0]}
		if len(parts) == 2 {
			p.Version = parts[1]
		}
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	return pkgs, nil
}

// captureNpm lists global npm packages via `npm ls -g --depth=0 --json`.
// We parse the JSON form because the human form is sensitive to npm's
// "minimal-output" experiments across versions.
func captureNpm(ctx context.Context, c provider.ContainerProvider, containerID string) ([]Package, error) {
	out, err := exec(ctx, c, containerID,
		"sh", "-c",
		`command -v npm >/dev/null 2>&1 && npm ls -g --depth=0 --json 2>/dev/null || true`)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var doc struct {
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		// npm ls can emit a partial doc with errors; fall through silently.
		return nil, nil
	}
	pkgs := make([]Package, 0, len(doc.Dependencies))
	for name, dep := range doc.Dependencies {
		pkgs = append(pkgs, Package{Name: name, Version: dep.Version})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	return pkgs, nil
}

// captureOS reads PRETTY_NAME from /etc/os-release so the snapshot
// records what distro / version the container is running. Useful when
// users compare snapshots across crews to see whether a base image
// drifted between provisions.
func captureOS(ctx context.Context, c provider.ContainerProvider, containerID string) (string, error) {
	out, err := exec(ctx, c, containerID,
		"sh", "-c",
		`. /etc/os-release 2>/dev/null && printf '%s' "$PRETTY_NAME" || true`)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// exec runs cmd inside containerID and returns the combined stdout/
// stderr as a string. Output is bounded to 1 MiB so a runaway probe
// can't OOM the orchestrator.
func exec(ctx context.Context, c provider.ContainerProvider, containerID string, cmd ...string) (string, error) {
	res, err := c.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         cmd,
	})
	if err != nil {
		return "", err
	}
	defer res.Reader.Close()
	const cap = 1 << 20
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(res.Reader, cap)); err != nil {
		return "", err
	}
	return buf.String(), nil
}
