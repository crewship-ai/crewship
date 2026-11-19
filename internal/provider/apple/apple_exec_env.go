package apple

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// execEnvFileFD is the descriptor number the env-file pipe gets in the
// `container` child: os/exec maps ExtraFiles[0] to fd 3.
const execEnvFileFD = 3

// execEnv is how one exec's environment reaches the `container` CLI without
// any value appearing in its argv (#5497). Host argv is readable by every user
// on the Mac (`ps -ww`), so `--env KEY=value` published every credential an
// exec carried.
//
// Two transports, both read by the CLI itself before it talks to the API
// server (Parser.allEnv in apple/container, unchanged from 1.0.0 to 1.7.1):
//
//   - --env-file /dev/fd/3, fed through an anonymous pipe. The parser opens the
//     path with FileHandle precisely so pipes and process substitution work
//     (its own comment: `--env-file <(echo "KEY=value")`, which on macOS is a
//     /dev/fd path). Nothing touches the disk.
//   - --env KEY with no "=", for which the CLI inherits the value from its own
//     process environment (cmd.Env) byte-exactly. Used only for values the
//     env-file parser would not carry unchanged (see envFileCanCarry). Kept
//     narrow, and refused for names the CLI itself reads (hostCLIReadsEnv),
//     because cmd.Env is also the CLI's own environment.
//
// Names must be ASCII identifiers on both paths, and the whole transport
// requires a CLI version whose parser was checked (checkExecEnvCLIVersion).
//
// A bare KEY entry in the caller's env already meant "inherit from the host"
// and carries no value, so it keeps its historic `--env KEY` form.
type execEnv struct {
	args    []string // CLI flags; never contain a value
	file    []byte   // env-file payload, nil when nothing goes through it
	inherit []string // KEY=value entries added to the CLI's environment
}

// minExecEnvCLIVersion is the oldest `container` release whose Parser.envFile
// and Parser.env were checked for this transport (1.0.0 through 1.7.1).
// Earlier releases differ: before 0.8.0 the env file is read with
// FileManager.contents(atPath:) behind a fileExists check (not verified to read
// a pipe), and 0.5.0 silently drops any value that contains "=".
var minExecEnvCLIVersion = [3]int{1, 0, 0}

var cliVersionRE = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// checkExecEnvCLIVersion fails closed when the detected CLI version is older
// than minExecEnvCLIVersion or cannot be read at all.
func checkExecEnvCLIVersion(version string) error {
	m := cliVersionRE.FindStringSubmatch(version)
	if m == nil {
		return fmt.Errorf("exec env: cannot read the container CLI version (%q); refusing to pass environment variables without a verified transport (need %d.%d.%d or newer)",
			version, minExecEnvCLIVersion[0], minExecEnvCLIVersion[1], minExecEnvCLIVersion[2])
	}
	var got [3]int
	for i := range got {
		if m[i+1] != "" {
			got[i], _ = strconv.Atoi(m[i+1])
		}
	}
	for i := range got {
		if got[i] != minExecEnvCLIVersion[i] {
			if got[i] < minExecEnvCLIVersion[i] {
				return fmt.Errorf("exec env: container CLI %s is older than %d.%d.%d, the oldest release whose environment transport is verified; upgrade Apple container",
					m[0], minExecEnvCLIVersion[0], minExecEnvCLIVersion[1], minExecEnvCLIVersion[2])
			}
			break
		}
	}
	return nil
}

// execEnvFor is the provider's entry point: the version gate, then
// buildExecEnv. An exec without environment entries needs neither.
func (p *Provider) execEnvFor(env []string) (execEnv, error) {
	if len(env) == 0 {
		return execEnv{}, nil
	}
	if err := checkExecEnvCLIVersion(p.cliVersion); err != nil {
		return execEnv{}, err
	}
	return buildExecEnv(env)
}

// buildExecEnv splits env (KEY=value entries, last duplicate wins, as with
// Docker and os/exec) across the two transports. It refuses, without ever
// echoing a value, any name the CLI could rewrite or act on itself.
func buildExecEnv(env []string) (execEnv, error) {
	var out execEnv
	order := make([]string, 0, len(env))
	values := make(map[string]string, len(env))
	bare := make(map[string]bool)
	for i, entry := range env {
		key, value, hasValue := strings.Cut(entry, "=")
		if !isEnvName(key) {
			// Only the position is reported: the name may carry the value
			// (an entry without "=" is all name) and Swift's parsers treat
			// non-ASCII names by grapheme cluster, not byte.
			return execEnv{}, fmt.Errorf("exec env: entry %d is not a valid variable name ([A-Za-z_][A-Za-z0-9_]*)", i+1)
		}
		if _, seen := values[key]; !seen && !bare[key] {
			order = append(order, key)
		}
		if hasValue {
			values[key] = value
			delete(bare, key)
		} else {
			bare[key] = true
			delete(values, key)
		}
	}

	var file strings.Builder
	for _, key := range order {
		if bare[key] {
			out.args = append(out.args, "--env", key)
			continue
		}
		value := values[key]
		if envFileCanCarry(value) {
			file.WriteString(key)
			file.WriteByte('=')
			file.WriteString(value)
			file.WriteByte('\n')
			continue
		}
		if hostCLIReadsEnv(key) {
			return execEnv{}, fmt.Errorf("exec env: %s cannot be delivered: its value needs the CLI's own environment, which the container CLI or Foundation reads under that name", key)
		}
		out.args = append(out.args, "--env", key)
		out.inherit = append(out.inherit, key+"="+value)
	}
	if file.Len() > 0 {
		out.file = []byte(file.String())
		out.args = append([]string{"--env-file", fmt.Sprintf("/dev/fd/%d", execEnvFileFD)}, out.args...)
	}
	return out, nil
}

// isEnvName reports whether name is an ASCII identifier. Anything else is
// refused outright: Parser.envFile strips leading Unicode whitespace from the
// name (renaming " PATH" to PATH after Crewship's own reserved-name checks)
// and reports an empty name together with the whole line, value included.
func isEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// envFileCanCarry reports whether value, after an ASCII-identifier name and
// "=", survives the CLI's env-file parser (Parser.envFile) unchanged.
//
// That parser works on Swift Characters (extended grapheme clusters) and
// finds the separator with firstIndex(of: "="). A value starting with a
// combining mark, ZWJ or variation selector fuses with the "=" into one
// cluster, so the separator disappears: the line becomes a bare name and is
// dropped, or fails with an error that prints it. An ASCII first byte cannot
// extend the preceding "=" cluster, and an ASCII name cannot extend it from
// the left. The parser also splits lines on Foundation's newline set and
// refuses invalid UTF-8.
func envFileCanCarry(value string) bool {
	if value != "" && value[0] >= utf8.RuneSelf {
		return false
	}
	if !utf8.ValidString(value) {
		return false
	}
	// CharacterSet.newlines: U+000A-U+000D, U+0085, U+2028, U+2029. NUL is
	// added so it keeps failing in os/exec as it did on argv.
	return !strings.ContainsAny(value, "\n\v\f\r\u0085\u2028\u2029\x00")
}

// hostCLIReadsEnv reports names the fallback must not set, because the value
// would also become the host `container` process's own environment. The CLI
// reads CONTAINER_* (CONTAINER_DEBUG, CONTAINER_APP_ROOT,
// CONTAINER_INSTALL_ROOT, CONTAINER_LOG_ROOT, CONTAINER_DEFAULT_PLATFORM, and
// PluginLoader forwards the whole prefix) and XDG_CONFIG_HOME (PathUtils);
// Foundation, dyld, libmalloc and XPC read the rest.
func hostCLIReadsEnv(name string) bool {
	switch name {
	case "HOME", "PATH", "TMPDIR", "SHELL", "USER":
		return true
	}
	for _, prefix := range []string{"CONTAINER_", "XDG_", "DYLD_", "LD_", "CF", "__CF", "NS", "XPC_", "Malloc"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// attach wires the transports into cmd before Start. The returned start
// function must replace cmd.Start: it starts the command and then feeds the
// env-file pipe, closing every descriptor this side holds on any path.
func (e execEnv) attach(cmd *exec.Cmd) (start func() error, err error) {
	if len(e.inherit) > 0 {
		// os/exec deduplicates Env with the last entry winning, so these
		// override any same-named host variable for this one invocation only.
		cmd.Env = append(os.Environ(), e.inherit...)
	}
	if e.file == nil {
		return cmd.Start, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("exec env pipe: %w", err)
	}
	cmd.ExtraFiles = []*os.File{r}
	return func() error {
		startErr := cmd.Start()
		// The child holds its own copy of the read end from here on.
		_ = r.Close()
		if startErr != nil {
			_ = w.Close()
			return startErr
		}
		// Written after Start so a payload larger than the pipe buffer cannot
		// deadlock. If the CLI exits without reading, the write fails with
		// EPIPE (Go does not die of SIGPIPE on non-stdio descriptors) and the
		// goroutine ends.
		go func() {
			_, _ = w.Write(e.file)
			_ = w.Close()
		}()
		return nil
	}, nil
}
