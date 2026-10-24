package skills

import (
	"context"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
)

type ownedGitResolver struct {
	answers []net.IPAddr
	calls   int
	host    string
}

func (r *ownedGitResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.calls++
	r.host = host
	return r.answers, nil
}

func TestGitImportDestinationPolicy(t *testing.T) {
	for _, tc := range []struct {
		ip            string
		allowed, want bool
	}{
		{"8.8.8.8", false, true}, {"10.0.0.2", false, false}, {"10.0.0.2", true, true},
		{"100.64.0.2", true, true}, {"fd12::1", true, true},
		{"127.0.0.1", true, false}, {"::1", true, false}, {"::ffff:127.0.0.1", true, false},
		{"169.254.169.254", true, false}, {"fd00:ec2::254", true, false}, {"fe80::1", true, false},
		{"0.0.0.0", true, false}, {"::", true, false}, {"224.0.0.1", true, false}, {"ff02::1", true, false},
	} {
		policy := GitImportPolicy{}
		if tc.allowed {
			policy.PrivateHosts = []string{"GIT.Example."}
		}
		resolver := &ownedGitResolver{answers: []net.IPAddr{{IP: net.ParseIP(tc.ip)}}}
		plan, err := planGitClone(context.Background(), "https://git.example/repo.git", policy, resolver)
		if (err == nil) != tc.want {
			t.Fatalf("%s allowed=%v: %v", tc.ip, tc.allowed, err)
		}
		if err == nil && (resolver.calls != 1 || plan.host != "git.example" || !plan.address.Equal(net.ParseIP(tc.ip))) {
			t.Fatal("wrong pinned plan")
		}
	}
	resolver := &ownedGitResolver{answers: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.2")}}}
	if _, err := planGitClone(context.Background(), "https://git.example/repo.git", GitImportPolicy{}, resolver); err == nil {
		t.Fatal("mixed public/private DNS answers accepted")
	}
	if _, err := planGitClone(context.Background(), "https://sub.git.example/repo.git", GitImportPolicy{PrivateHosts: []string{"git.example"}}, &ownedGitResolver{answers: []net.IPAddr{{IP: net.ParseIP("10.0.0.2")}}}); err == nil {
		t.Fatal("allowlist became suffix wildcard")
	}
}

func TestGitHostCanonicalizationAndNumericAmbiguity(t *testing.T) {
	for _, host := range []string{"127.1", "2130706433", "0177.0.0.1", "0x7f000001", "0X7F.0.0.1", "１２７.１", "bad..host", "*.example", "git.example:443", "git.example/path", "fe80::1%eth0"} {
		if _, err := canonicalGitHost(host); err == nil {
			t.Fatalf("ambiguous host accepted: %q", host)
		}
		if err := ValidateGitImportPolicy(GitImportPolicy{PrivateHosts: []string{host}}); err == nil {
			t.Fatalf("ambiguous allowlist host accepted: %q", host)
		}
	}
	for host, want := range map[string]string{"GIT.Example.": "git.example", "bücher.example": "xn--bcher-kva.example", "8.8.8.8": "8.8.8.8", "2001:4860:4860::8888": "2001:4860:4860::8888"} {
		got, err := canonicalGitHost(host)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
	resolver := &ownedGitResolver{}
	for _, raw := range []string{"https://localhost./repo.git", "https://127.1/repo.git", "https://[fe80::1%25eth0]/repo.git", "https://user:pass@git.example/repo.git", "http://git.example/repo.git"} {
		if _, err := planGitClone(context.Background(), raw, GitImportPolicy{}, resolver); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
	if resolver.calls != 0 {
		t.Fatal("unsafe spelling reached DNS")
	}
}

func TestGitEnvironmentAndBoundedOutput(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.sslVerify")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_SSL_NO_VERIFY", "true")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("GIT_TRACE", "1")
	env := isolatedGitEnv("/usr/bin/git", t.TempDir())
	for _, entry := range env {
		for _, prefix := range []string{"GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_", "GIT_SSL_NO_VERIFY=", "HTTPS_PROXY=", "GIT_TRACE="} {
			if strings.HasPrefix(entry, prefix) {
				t.Fatalf("inherited override %s", prefix)
			}
		}
	}
	out := &cappedGitOutput{limit: 8}
	n, err := io.Copy(out, strings.NewReader(strings.Repeat("x", 1<<20)))
	if err != nil || n != 1<<20 || out.String() != strings.Repeat("x", 8) {
		t.Fatalf("output cap/drain failed: %d %v", n, err)
	}
	plan := gitClonePlan{url: "https://git.example:443/repo.git", host: "git.example", port: "443", address: net.ParseIP("2001:4860:4860::8888")}
	args, err := pinnedGitCloneArgs(plan, "owned-template", "owned-destination", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, "\n"), "http.curloptResolve=git.example:443:[2001:4860:4860::8888]") {
		t.Fatal("IPv6 pin missing or malformed")
	}
	if !reflect.DeepEqual(args[len(args)-3:], []string{"--", plan.url, "owned-destination"}) {
		t.Fatal("missing URL option separator")
	}
}

func TestPinnedGitVersionStrings(t *testing.T) {
	for _, s := range []string{"git version 2.43.0", "git version 2.49.0.windows.1", "git version 2.39.5 (Apple Git-154)"} {
		if gitVersionPattern.FindStringSubmatch(s) == nil {
			t.Fatalf("real version rejected: %s", s)
		}
	}
}

func TestGitImporterSnapshotsAdministratorPolicy(t *testing.T) {
	policy := GitImportPolicy{PrivateHosts: []string{"git.internal.example"}}
	original := newCovImporter(t)
	imp, err := NewImporterWithGitPolicy(original.db, original.logger, policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.PrivateHosts[0] = "attacker.example"
	if imp.gitPolicy.PrivateHosts[0] != "git.internal.example" {
		t.Fatal("mutable caller policy changed importer authority")
	}
	if _, err := NewImporterWithGitPolicy(original.db, original.logger, GitImportPolicy{PrivateHosts: []string{"*.example"}}); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
