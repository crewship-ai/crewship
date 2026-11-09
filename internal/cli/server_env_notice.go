package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// IgnoredServerEnvNotice explains, in one line for stderr, that CREWSHIP_SERVER
// is set but an implicitly selected profile (a directory binding or the
// `server use` default) sends this command to a different server. The
// precedence itself is unchanged (see EffectiveServer): the profile's token is
// bound to the profile's server. It returns "" when there is nothing to say:
// no env, --server given, a profile chosen explicitly (--profile or
// CREWSHIP_PROFILE), legacy mode, or the same server.
func IgnoredServerEnvNotice(flagServer, flagProfile string, cfg *CLIConfig) string {
	env := strings.TrimSpace(os.Getenv("CREWSHIP_SERVER"))
	if env == "" || flagServer != "" || cfg == nil {
		return ""
	}
	name, source := ActiveProfileNameWithSource(flagProfile, cfg)
	if name == "" || (source != ProfileSourceDirectory && source != ProfileSourcePersisted) {
		return ""
	}
	server := EffectiveServer(flagServer, flagProfile, cfg)
	if server == "" || sameServer(env, server) {
		return ""
	}
	why := "the `crewship server use` default"
	if source == ProfileSourceDirectory {
		why = "bound to this directory"
	}
	return fmt.Sprintf("warning: CREWSHIP_SERVER=%s is ignored; profile %q (%s) sends this command to %s. Use --profile, --server or CREWSHIP_PROFILE to choose the server.", env, name, why, server)
}

// sameServer compares scheme, host and port, treating loopback names as one host.
func sameServer(a, b string) bool {
	key := func(raw string) string {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
		}
		host, port := strings.ToLower(u.Hostname()), u.Port()
		if host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.") {
			host = "loopback"
		}
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
		}
		return strings.ToLower(u.Scheme) + "://" + host + ":" + port
	}
	return key(a) == key(b)
}
