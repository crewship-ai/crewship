package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// DetectResult contains info about the Apple Container runtime.
type DetectResult struct {
	Version string // CLI version
	HostIP  string // IP address the host is reachable at from containers
}

// Detect probes for the Apple Container CLI and checks that the system
// service is running. Returns an error if Apple Containers are not available.
func Detect(ctx context.Context) (*DetectResult, error) {
	// Check if the `container` binary exists
	_, err := exec.LookPath("container")
	if err != nil {
		return nil, fmt.Errorf("apple container CLI not found: %w", err)
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
