package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// DetectResult contains info about the Apple Container runtime.
type DetectResult struct {
	Version string // CLI version
	HostIP  string // IP address the host is reachable at from containers
}

// minMacOSMajor is the macOS major version Apple's container runtime needs for
// the networking model this provider is written against: every container gets
// its own IPv4 address, routed through the host, which is what
// discoverHostIP and the containerJSON networks parsing assume. Older macOS
// releases can have the CLI installed and its system service answering while
// providing none of that, so probing only the binary and the service reports a
// runtime that cannot actually host a crew (#1647).
const minMacOSMajor = 26

// Detect probes for the Apple Container CLI and checks that the system
// service is running. Returns an error if Apple Containers are not available.
func Detect(ctx context.Context) (*DetectResult, error) {
	// Check if the `container` binary exists
	_, err := exec.LookPath("container")
	if err != nil {
		return nil, fmt.Errorf("apple container CLI not found: %w", err)
	}

	// The host has to be new enough for the runtime's networking model
	if err := checkMacOSProductVersion(macOSProductVersion(ctx)); err != nil {
		return nil, err
	}

	// Get version
	version, err := getVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("apple container version: %w", err)
	}

	// Check system status (is the apiserver running?)
	if err := checkSystemStatus(ctx); err != nil {
		return nil, fmt.Errorf("apple container system not running: %w", err)
	}

	// Discover host IP (the IP containers can use to reach the host)
	hostIP := discoverHostIP()

	return &DetectResult{
		Version: version,
		HostIP:  hostIP,
	}, nil
}

// macOSProductVersion reports the host's macOS product version (e.g. "26.1"),
// or "" when it cannot be read — sw_vers is absent off macOS and its output is
// Apple's to change.
func macOSProductVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "sw_vers", "-productVersion")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

// checkMacOSProductVersion rejects hosts older than minMacOSMajor. It compares
// the major component numerically, so every future macOS satisfies the gate —
// a lexical compare would read "9.9" as newer than "26.0" and would start
// failing outright the first time the major grows a digit.
//
// A version it cannot parse is not evidence of an old host, so it fails open
// and lets the CLI and system-status probes decide.
func checkMacOSProductVersion(productVersion string) error {
	major, ok := macOSMajor(productVersion)
	if !ok {
		return nil
	}
	if major < minMacOSMajor {
		return fmt.Errorf("apple container runtime requires macOS %d or newer, host reports %s",
			minMacOSMajor, strings.TrimSpace(productVersion))
	}
	return nil
}

// macOSMajor extracts the leading major version number, reporting false when
// the string is not a version this code recognises.
func macOSMajor(productVersion string) (int, bool) {
	head, _, _ := strings.Cut(strings.TrimSpace(productVersion), ".")
	major, err := strconv.Atoi(head)
	if err != nil || major <= 0 {
		return 0, false
	}
	return major, true
}

func getVersion(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "container", "system", "version", "--format", "json")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		// Fallback: try without --format
		cmd2 := exec.CommandContext(ctx, "container", "system", "version")
		var stdout2 bytes.Buffer
		cmd2.Stdout = &stdout2
		if err2 := cmd2.Run(); err2 != nil {
			return "", fmt.Errorf("version: %w", err)
		}
		return strings.TrimSpace(stdout2.String()), nil
	}

	// JSON output is an array: [{"appName":"container","version":"0.10.0",...}, ...]
	var versionArr []struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &versionArr); err == nil {
		for _, v := range versionArr {
			if v.AppName == "container" {
				return v.Version, nil
			}
		}
		if len(versionArr) > 0 {
			return versionArr[0].Version, nil
		}
	}

	// Fallback: try as single object
	var single struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &single); err == nil && single.Version != "" {
		return single.Version, nil
	}

	return strings.TrimSpace(stdout.String()), nil
}

func checkSystemStatus(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "container", "system", "status")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("system status check failed: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// discoverHostIP finds a local IP address that containers can reach.
// Apple Containers get dedicated IPs and route through the host,
// so the host's primary interface IP works.
func discoverHostIP() string {
	// Try to find the primary non-loopback IPv4 address
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ip := ipNet.IP.String()
				// Prefer en0-style addresses (192.168.x.x, 10.x.x.x)
				if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") {
					return ip
				}
			}
		}
	}
	// Fallback: return any non-loopback IPv4
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}
