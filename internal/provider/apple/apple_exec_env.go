package apple

import (
	"fmt"
	"os"
	"os/exec"
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
//     process environment (cmd.Env). Used only for values the env-file format
//     cannot carry: it splits lines on every line-break character and requires
//     valid UTF-8. Kept narrow because cmd.Env is also the CLI's own
//     environment and a container PATH or HOME there could change how the CLI
//     itself behaves.
//
// A bare KEY entry in the caller's env already meant "inherit from the host"
// and carries no value, so it keeps its historic `--env KEY` form.
type execEnv struct {
	args    []string // CLI flags; never contain a value
	file    []byte   // env-file payload, nil when nothing goes through it
	inherit []string // KEY=value entries added to the CLI's environment
}

// buildExecEnv splits env (KEY=value entries, last duplicate wins, as with
// Docker and os/exec) across the two transports.
func buildExecEnv(env []string) (execEnv, error) {
	var out execEnv
	order := make([]string, 0, len(env))
	values := make(map[string]string, len(env))
	bare := make(map[string]bool)
	for _, entry := range env {
		key, value, hasValue := strings.Cut(entry, "=")
		if key == "" {
			// The value is never echoed: it may be the credential itself.
			return execEnv{}, fmt.Errorf("exec env: entry with an empty variable name")
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
		if envFileCanCarry(key, value) {
			file.WriteString(key)
			file.WriteByte('=')
			file.WriteString(value)
			file.WriteByte('\n')
			continue
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

// envFileCanCarry reports whether KEY=value survives the CLI's env-file parser
// (Parser.envFile, a port of Moby's kvfile) unchanged. That parser drops lines
// starting with '#', strips leading whitespace, rejects whitespace inside the
// name, splits on Foundation's newline set and refuses invalid UTF-8.
func envFileCanCarry(key, value string) bool {
	if strings.HasPrefix(key, "#") || strings.ContainsAny(key, " \t") {
		return false
	}
	entry := key + "=" + value
	if !utf8.ValidString(entry) {
		return false
	}
	// CharacterSet.newlines: U+000A–U+000D, U+0085, U+2028, U+2029. NUL is
	// added so it keeps failing in os/exec as it did on argv.
	return !strings.ContainsAny(entry, "\n\v\f\r\u0085  \x00")
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
