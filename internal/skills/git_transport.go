package skills

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// GitImportPolicy is administrator-owned. It never comes from import requests.
// PrivateHosts permits private destinations only for exact canonical hosts.
// Loopback, link-local, metadata and other hard-blocked addresses stay denied.
type GitImportPolicy = httpsafe.GitImportPolicy

type gitIPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type gitClonePlan struct {
	url     string
	host    string
	port    string
	address net.IP
	literal bool
	// Only internal native-Git tests provide their owned CA. Production uses
	// the system trust store, with certificate verification always enabled.
	caFile string
}

func canonicalGitHost(raw string) (string, error) { return httpsafe.CanonicalGitHost(raw) }

func ValidateGitImportPolicy(policy GitImportPolicy) error {
	return httpsafe.ValidateGitImportPolicy(policy)
}

func planGitClone(ctx context.Context, raw string, policy GitImportPolicy, resolver gitIPResolver) (gitClonePlan, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return gitClonePlan{}, fmt.Errorf("parse git URL: invalid URL")
	}
	if u.Scheme != "https" || u.Opaque != "" {
		return gitClonePlan{}, fmt.Errorf("bulk import requires https git URLs")
	}
	if u.User != nil {
		return gitClonePlan{}, fmt.Errorf("git URL must not embed credentials")
	}
	host, err := canonicalGitHost(u.Hostname())
	if err != nil {
		return gitClonePlan{}, err
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return gitClonePlan{}, fmt.Errorf("localhost git URLs are not allowed")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return gitClonePlan{}, fmt.Errorf("invalid git URL port")
	}
	port = strconv.Itoa(n)
	allowed := false
	for _, entry := range policy.PrivateHosts {
		canonical, err := canonicalGitHost(entry)
		if err != nil {
			return gitClonePlan{}, fmt.Errorf("invalid administrator git import policy")
		}
		if canonical == host {
			allowed = true
		}
	}
	if !allowed {
		if err := validateGitURL(raw); err != nil {
			return gitClonePlan{}, err
		}
	}
	var addresses []net.IPAddr
	literal := net.ParseIP(host)
	if literal != nil {
		addresses = []net.IPAddr{{IP: literal}}
	} else {
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		addresses, err = resolver.LookupIPAddr(lookupCtx, host)
		if err != nil {
			return gitClonePlan{}, fmt.Errorf("git import DNS resolution failed")
		}
	}
	if len(addresses) == 0 {
		return gitClonePlan{}, fmt.Errorf("git import DNS returned no addresses")
	}
	for _, address := range addresses {
		ip := address.IP
		if ip == nil || address.Zone != "" || ip.IsLoopback() || httpsafe.IsHardBlockedIP(ip) || !allowed && httpsafe.IsBlockedIP(ip) {
			return gitClonePlan{}, fmt.Errorf("private/internal IP or forbidden destination in git URL")
		}
	}
	// All answers are checked, and only one approved address is used. Failure
	// never falls back to another DNS lookup or a weaker transport.
	u.Host = net.JoinHostPort(host, port)
	return gitClonePlan{url: u.String(), host: host, port: port, address: addresses[0].IP, literal: literal != nil}, nil
}

func isolatedGitEnv(gitPath, home string) []string {
	env := []string{
		"PATH=" + filepath.Dir(gitPath) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home, "CURL_HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=",
		"LANG=C", "LC_ALL=C",
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		env = append(env, "SystemRoot="+root)
	}
	return env
}

// cappedGitOutput drains all output while retaining a bounded diagnostic.
// stdout/stderr use the same writer, so os/exec serializes their writes.
type cappedGitOutput struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedGitOutput) String() string { return b.buf.String() }
func (b *cappedGitOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}
func runIsolatedGit(ctx context.Context, gitPath, home string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Env = isolatedGitEnv(gitPath, home)
	cmd.Dir = home
	cmd.WaitDelay = 2 * time.Second
	configureGitProcess(cmd)
	out := &cappedGitOutput{limit: 256 * 1024}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	return out.String(), err
}

var gitVersionPattern = regexp.MustCompile(`^git version ([0-9]+)\.([0-9]+)\.([0-9]+)(?:[. -].*)?$`)

func requirePinnedGit(ctx context.Context, gitPath, home string) error {
	version, err := runIsolatedGit(ctx, gitPath, home, "--version")
	if err != nil {
		return fmt.Errorf("bulk import via git_url requires a working Git >=2.37")
	}
	match := gitVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return fmt.Errorf("bulk import via git_url requires a recognized Git >=2.37")
	}
	major, e1 := strconv.Atoi(match[1])
	minor, e2 := strconv.Atoi(match[2])
	if e1 != nil || e2 != nil || major < 2 || major == 2 && minor < 37 {
		return fmt.Errorf("bulk import via git_url requires Git >=2.37 for pinned HTTPS connections")
	}
	config, err := runIsolatedGit(ctx, gitPath, home, "--no-pager", "help", "--config")
	if err != nil {
		return fmt.Errorf("bulk import via git_url cannot verify pinned HTTPS support")
	}
	for _, key := range strings.Fields(config) {
		if key == "http.curloptResolve" {
			return nil
		}
	}
	return fmt.Errorf("bulk import via git_url requires Git http.curloptResolve support")
}

func pinnedGitCloneArgs(plan gitClonePlan, template, destination, ref string) ([]string, error) {
	if plan.address == nil {
		return nil, fmt.Errorf("git import missing approved destination")
	}
	args := []string{"-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "http.proxy=",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "credential.helper=",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "init.templateDir=" + template}
	if !plan.literal {
		address := plan.address.String()
		if strings.Contains(address, ":") {
			address = "[" + address + "]"
		}
		args = append(args, "-c", "http.curloptResolve="+plan.host+":"+plan.port+":"+address)
	}
	if plan.caFile != "" {
		args = append(args, "-c", "http.sslCAInfo="+plan.caFile)
	}
	args = append(args, "clone", "--depth", "1", "--filter=blob:none", "--single-branch")
	if ref != "" {
		if err := validateGitRef(ref); err != nil {
			return nil, err
		}
		args = append(args, "--branch", ref)
	}
	return append(args, "--", plan.url, destination), nil
}
