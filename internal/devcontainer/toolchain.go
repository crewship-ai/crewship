package devcontainer

import (
	"archive/tar"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const toolchainDirectory = "/opt/crewship/toolchain"
const toolchainArchiveLimit = 128 << 10

// ToolchainInventory describes observations from an image, never the mutable
// filesystem of a running agent. Unavailable evidence is not a resolved version.
type ToolchainQualification struct {
	Status  string           `json:"status" yaml:"status"`
	ImageID string           `json:"image_id" yaml:"image_id"`
	Tools   []ToolchainProbe `json:"tools" yaml:"tools"`
}

type ToolchainProbe struct {
	Binary string `json:"binary" yaml:"binary"`
	Status string `json:"status" yaml:"status"`
}

type ToolchainInventory struct {
	Qualification *ToolchainQualification `json:"qualification,omitempty" yaml:"qualification,omitempty"`
	SchemaVersion int                     `json:"schema_version" yaml:"schema_version"`
	Status        string                  `json:"status" yaml:"status"`
	ImageID       string                  `json:"image_id,omitempty" yaml:"image_id,omitempty"`
	Tools         []ToolchainTool         `json:"tools" yaml:"tools"`
}

type ToolchainTool struct {
	Binary  string `json:"binary" yaml:"binary"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	Path    string `json:"path,omitempty" yaml:"path,omitempty"`
	Status  string `json:"status" yaml:"status"`
}

var toolVersionPattern = regexp.MustCompile(`^v?([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)$`)

// ObservedToolVersion accepts known --version shapes only. Arbitrary stdout, diagnostics and URLs
// must not become status API fields or leak into a stored inventory.
func ObservedToolVersion(binary, output string) string {
	line := strings.TrimSpace(output)
	if len(line) > 128 || strings.ContainsAny(line, "\r\n") {
		return ""
	}
	switch binary {
	case "claude":
		line = strings.TrimSuffix(line, " (Claude Code)")
	case "codex":
		line = strings.TrimPrefix(line, "codex-cli ")
	case "droid":
		line = strings.TrimPrefix(line, "droid ")
	case "gemini", "opencode", "cursor-agent":
	default:
		return ""
	}
	match := toolVersionPattern.FindStringSubmatch(line)
	if match == nil {
		return ""
	}
	return match[1]
}

func unknownToolchain(bins []string) *ToolchainInventory {
	result := &ToolchainInventory{SchemaVersion: 1, Status: "unavailable", Tools: []ToolchainTool{}}
	for _, binary := range SortedBinaries(bins) {
		result.Tools = append(result.Tools, ToolchainTool{Binary: binary, Status: "unavailable"})
	}
	return result
}

// parseToolchainArchive reads a bounded tar stream without extracting files on
// the host. Only ordinary inventory files are accepted; links and traversal are
// rejected, and raw probe output is never returned to callers.
func parseToolchainArchive(r io.Reader, bins []string) (*ToolchainInventory, error) {
	limited := &io.LimitedReader{R: r, N: toolchainArchiveLimit + 1}
	tr := tar.NewReader(limited)
	files := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid toolchain archive")
		}
		if limited.N <= 0 || len(files) > 64 {
			return nil, fmt.Errorf("toolchain archive exceeds limit")
		}
		clean := path.Clean(header.Name)
		if clean != strings.TrimSuffix(header.Name, "/") || path.IsAbs(clean) || (clean != "toolchain" && !strings.HasPrefix(clean, "toolchain/")) {
			return nil, fmt.Errorf("invalid toolchain archive path")
		}
		if header.Typeflag == tar.TypeDir && clean == "toolchain" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 8192 {
			return nil, fmt.Errorf("invalid toolchain archive entry")
		}
		name := strings.TrimPrefix(clean, "toolchain/")
		if strings.Contains(name, "/") || name == "toolchain" {
			return nil, fmt.Errorf("invalid toolchain file")
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("duplicate toolchain file")
		}
		raw, err := io.ReadAll(tr)
		if err != nil || limited.N <= 0 {
			return nil, fmt.Errorf("invalid toolchain file content")
		}
		files[name] = string(raw)
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("toolchain archive exceeds limit")
	}
	if strings.TrimSpace(files["schema"]) != "1" {
		return nil, fmt.Errorf("unsupported toolchain schema")
	}
	result := unknownToolchain(bins)
	result.Status = "recorded"
	for i := range result.Tools {
		tool := &result.Tools[i]
		status, found := files[tool.Binary+".status"]
		if !found {
			continue
		}
		if strings.TrimSpace(status) != "0" {
			tool.Status = "probe_failed"
			continue
		}
		tool.Version = ObservedToolVersion(tool.Binary, files[tool.Binary+".version"])
		if tool.Version == "" {
			tool.Status = "unrecognized_version"
			continue
		}
		executable := strings.TrimSpace(files[tool.Binary+".path"])
		if !strings.HasPrefix(executable, "/") || len(executable) > 512 || strings.ContainsAny(executable, "\x00\r\n\t") {
			tool.Version = ""
			tool.Status = "unavailable"
			continue
		}
		tool.Path = executable
		tool.Status = "observed"
	}
	return result, nil
}

// ToolchainRequest reports declarative selectors, not inferred installed
// versions. Feature-provided tools have no mise selector to claim.
type ToolchainRequest struct {
	Adapter  string `json:"adapter" yaml:"adapter"`
	Binary   string `json:"binary" yaml:"binary"`
	Source   string `json:"source" yaml:"source"`
	Selector string `json:"selector,omitempty" yaml:"selector,omitempty"`
	Exact    bool   `json:"exact" yaml:"exact"`
}

func RequestedToolchain(cfg *Config, miseConfig string, adapters []string) ([]ToolchainRequest, error) {
	var mise *MiseConfig
	if strings.TrimSpace(miseConfig) != "" {
		var err error
		mise, err = ParseMiseConfig(miseConfig)
		if err != nil {
			return nil, err
		}
	}
	result := []ToolchainRequest{}
	for _, cli := range RequiredAdapterCLIs(adapters) {
		request := ToolchainRequest{Adapter: cli.Adapter, Binary: cli.Binary}
		switch {
		case mise != nil && cli.MiseTool != "" && mise.Tools[cli.MiseTool] != "":
			// Explicit mise input is installed after features and its shim takes
			// precedence. Do not hide a user's pin behind an overlapping feature.
			request.Source = "mise"
			request.Selector = mise.Tools[cli.MiseTool]
			request.Exact = toolVersionPattern.MatchString(request.Selector)
		case cfg != nil && featureProvidesBinary(cfg, cli):
			request.Source = "devcontainer_feature"
		case cli.MiseTool != "":
			request.Source = "mise"
			request.Selector = "latest"
			if mise != nil && mise.Tools[cli.MiseTool] != "" {
				request.Selector = mise.Tools[cli.MiseTool]
			}
			request.Exact = toolVersionPattern.MatchString(request.Selector)
		default:
			request.Source = "installer"
		}
		result = append(result, request)
	}
	return result, nil
}
