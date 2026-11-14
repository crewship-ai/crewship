package craftassets

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// Set by the release build (goreleaser ldflags). An ordinary `go build` sets
// neither, so it trusts no package until an operator names one.
var (
	// releaseCatalog lists the packages published with this release:
	// "<application>=<version>=<manifest sha256>", comma separated.
	releaseCatalog string
	// releaseBase is the URL the release's packages are published under.
	releaseBase string
)

// Pin is one package this binary trusts.
type Pin struct {
	Application    string `json:"application"`
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

// AssetName is the file name a package is published under.
func AssetName(application string) string { return "crewship-craft-" + application + ".craftpkg" }

// Catalog is the set of trusted packages and where to fetch them.
type Catalog struct {
	Pins map[string]Pin
	// BaseURL is where AssetName(application) is downloaded from; empty means
	// packages can only be installed from a file.
	BaseURL string
	// Operator is true when the pins come from server configuration instead
	// of this release. Configuration that can set them decides what Crewship
	// trusts, as much as the choice of binary does.
	Operator bool
}

// URL is the download address of the application's package.
func (c Catalog) URL(application string) string {
	if c.BaseURL == "" {
		return ""
	}
	return strings.TrimSuffix(c.BaseURL, "/") + "/" + AssetName(application)
}

// ReleaseCatalog is the catalog compiled into this binary. Operator
// configuration may replace either part (a development build, a mirror).
func ReleaseCatalog(pins, baseURL string) (Catalog, error) {
	operator := pins != ""
	if !operator {
		pins = releaseCatalog
	}
	if baseURL == "" {
		baseURL = releaseBase
	}
	parsed, err := ParsePins(pins)
	if err != nil {
		return Catalog{}, err
	}
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		// http only to this machine (a local mirror, tests); the pins protect
		// the bytes either way, but a remote mirror must use https.
		local := u != nil && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback())
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && local)) || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Catalog{}, fmt.Errorf("invalid Craft component download URL %q", baseURL)
		}
	}
	return Catalog{Pins: parsed, BaseURL: baseURL, Operator: operator && len(parsed) > 0}, nil
}

// ParsePins reads "<application>=<version>=<manifest sha256>[,…]".
func ParsePins(s string) (map[string]Pin, error) {
	out := map[string]Pin{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, item := range strings.Split(s, ",") {
		parts := strings.Split(strings.TrimSpace(item), "=")
		if len(parts) != 3 || !known(parts[0]) || !versionPattern(parts[1]) || !sha256Pattern.MatchString(parts[2]) {
			return nil, fmt.Errorf("invalid Craft component pin %q", item)
		}
		if _, dup := out[parts[0]]; dup {
			return nil, fmt.Errorf("Craft component %s pinned twice", parts[0])
		}
		out[parts[0]] = Pin{Application: parts[0], Version: parts[1], ManifestSHA256: parts[2]}
	}
	return out, nil
}

// String renders pins in the ParsePins format.
func String(pins map[string]Pin) string {
	var items []string
	for _, p := range pins {
		items = append(items, p.Application+"="+p.Version+"="+p.ManifestSHA256)
	}
	sort.Strings(items)
	return strings.Join(items, ",")
}

func versionPattern(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune(".+-_", r)) {
			return false
		}
	}
	return true
}

func known(app string) bool {
	for _, a := range Applications {
		if a == app {
			return true
		}
	}
	return false
}
