package docker

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Compare kernel-reported option sets, not inspect's requested driver options.
func canonicalMountFlags(options string) (string, error) {
	flags := map[string]bool{}
	for _, option := range strings.Split(options, ",") {
		option = strings.TrimSpace(option)
		if option != "" {
			flags[option] = true
		}
	}
	if flags["rw"] == flags["ro"] {
		return "", fmt.Errorf("mount flags require exactly one access mode: %q", options)
	}
	sorted := make([]string, 0, len(flags))
	for option := range flags {
		sorted = append(sorted, option)
	}
	sort.Strings(sorted)
	return strings.Join(sorted, ","), nil
}

func nativeDataMountFlags(raw string) (map[string]string, error) {
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		target, options, ok := strings.Cut(line, "=")
		if !ok || (target != "/workspace" && target != "/output" && target != "/crew") {
			return nil, fmt.Errorf("invalid native mount observation %q", line)
		}
		if _, exists := result[target]; exists {
			return nil, fmt.Errorf("duplicate native mount observation %s", target)
		}
		canonical, err := canonicalMountFlags(options)
		if err != nil {
			return nil, err
		}
		result[target] = canonical
	}
	if len(result) != 3 {
		return nil, fmt.Errorf("native baseline must observe all three data mounts")
	}
	return result, nil
}

func compareDataMountFlags(baseline map[string]string, target, options string) error {
	expected, ok := baseline[target]
	if !ok {
		return fmt.Errorf("native baseline missing %s", target)
	}
	actual, err := canonicalMountFlags(options)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("mapped %s flags differ from native: native=%s mapped=%s", target, expected, actual)
	}
	return nil
}

func TestDataMountFlagParity(t *testing.T) {
	for _, native := range []string{"rw,relatime", "rw,nosuid,noexec,relatime"} {
		baseline, err := nativeDataMountFlags("/workspace=" + native + "\n/output=" + native + "\n/crew=" + native)
		if err != nil {
			t.Fatal(err)
		}
		if err := compareDataMountFlags(baseline, "/workspace", native); err != nil {
			t.Fatal(err)
		}
		if err := compareDataMountFlags(baseline, "/output", "ro,nosuid,noexec,relatime"); err == nil {
			t.Fatal("different effective flags accepted")
		}
	}
	baseline, err := nativeDataMountFlags("/workspace=rw,nosuid,noexec\n/output=rw,nosuid,noexec\n/crew=rw,nosuid,noexec")
	if err != nil {
		t.Fatal(err)
	}
	if err := compareDataMountFlags(baseline, "/crew", "noexec,rw,nosuid"); err != nil {
		t.Fatal(err)
	}
	if err := compareDataMountFlags(baseline, "/crew", "rw,nosuid"); err == nil {
		t.Fatal("lost effective restriction accepted")
	}
	for _, invalid := range []string{"", "/workspace=rw", "/workspace=rw\n/workspace=rw\n/crew=rw", "/workspace=ro,rw\n/output=rw\n/crew=rw"} {
		if _, err := nativeDataMountFlags(invalid); err == nil {
			t.Fatalf("invalid baseline accepted: %q", invalid)
		}
	}
}
