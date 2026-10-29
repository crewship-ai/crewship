package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

var codeNamespaces = []string{"inputs", "steps", "run", "env", "routine", "secrets", "event"}

func codeNamespace(name string) bool {
	for _, allowed := range codeNamespaces {
		if name == allowed {
			return true
		}
	}
	return false
}

func codePathPart(part string) bool {
	if part == "" {
		return false
	}
	for _, r := range part {
		if r != '_' && r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func codeTemplateError() error {
	return fmt.Errorf("code templates must be simple typed references: use inputs.x or steps.id.output directly; embedded text and expressions inside {{ }} are not supported")
}

// Only AUTHOR-written path segments enter the returned source. Runtime values
// never pass through this function. Brackets also support hyphenated step IDs.
func codeReference(ref string) (string, error) {
	parts := strings.Split(strings.TrimSpace(ref), ".")
	if len(parts) < 2 || !codeNamespace(parts[0]) {
		return "", codeTemplateError()
	}
	if parts[0] == "steps" && len(parts) < 3 {
		return "", codeTemplateError()
	}
	var out strings.Builder
	out.WriteString(parts[0])
	for _, part := range parts[1:] {
		if !codePathPart(part) {
			return "", codeTemplateError()
		}
		out.WriteByte('[')
		out.WriteString(strconv.Quote(part))
		out.WriteByte(']')
	}
	return out.String(), nil
}

func codeTemplateAt(source string, start int) (string, int, error) {
	end := strings.Index(source[start+2:], "}}")
	if end < 0 {
		return "", 0, codeTemplateError()
	}
	end += start + 2
	ref, err := codeReference(source[start+2 : end])
	return ref, end + 2, err
}

// normalizeCodeSource is a runtime-only compatibility translation, not Render.
// Ordinary quoted whole references are unwrapped into typed selectors. Other
// string interpolation is rejected. CEL raw/triple/bytes literals are preserved
// byte-for-byte when they contain no template, and explicitly rejected otherwise.
func normalizeCodeSource(source string) (string, error) {
	if !strings.Contains(source, "{{") {
		return source, nil
	}
	var out strings.Builder
	for i := 0; i < len(source); {
		if strings.HasPrefix(source[i:], "//") {
			end := strings.IndexByte(source[i:], '\n')
			if end < 0 {
				end = len(source) - i
			}
			// Translate syntax in comments without treating comment quotes as
			// literals. No values are resolved, including unused references.
			comment := source[i : i+end]
			for {
				start := strings.Index(comment, "{{")
				if start < 0 {
					out.WriteString(comment)
					break
				}
				out.WriteString(comment[:start])
				ref, next, err := codeTemplateAt(comment, start)
				if err != nil {
					return "", err
				}
				out.WriteString(ref)
				comment = comment[next:]
			}
			i += end
			continue
		}
		quote, raw, literal := codeLiteralStart(source, i)
		if literal {
			delim := source[quote : quote+1]
			if strings.HasPrefix(source[quote:], strings.Repeat(delim, 3)) {
				delim = strings.Repeat(delim, 3)
			}
			end := quote + len(delim)
			for end < len(source) && !strings.HasPrefix(source[end:], delim) {
				if !raw && source[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(source) {
				return "", codeTemplateError()
			}
			body := source[quote+len(delim) : end]
			if strings.Contains(body, "{{") {
				if quote != i || len(delim) != 1 || !strings.HasPrefix(body, "{{") {
					return "", codeTemplateError()
				}
				ref, next, err := codeTemplateAt(body, 0)
				if err != nil || next != len(body) {
					return "", codeTemplateError()
				}
				out.WriteString(ref)
			} else {
				out.WriteString(source[i : end+len(delim)])
			}
			i = end + len(delim)
			continue
		}
		if strings.HasPrefix(source[i:], "{{") {
			ref, next, err := codeTemplateAt(source, i)
			if err != nil {
				return "", err
			}
			out.WriteString(ref)
			i = next
			continue
		}
		out.WriteByte(source[i])
		i++
	}
	return out.String(), nil
}

func codeLiteralStart(source string, start int) (quote int, raw, ok bool) {
	quote = start
	if source[quote] == '"' || source[quote] == '\'' {
		return quote, false, true
	}
	// Prefixes are CEL tokens only at an identifier boundary.
	if start > 0 {
		prev := source[start-1]
		if prev >= 'a' && prev <= 'z' || prev >= 'A' && prev <= 'Z' || prev >= '0' && prev <= '9' || prev == '_' {
			return 0, false, false
		}
	}
	if source[quote] == 'b' || source[quote] == 'B' {
		quote++
	}
	if quote < len(source) && (source[quote] == 'r' || source[quote] == 'R') {
		raw = true
		quote++
	}
	if quote == start || quote >= len(source) {
		return 0, false, false
	}
	return quote, raw, source[quote] == '"' || source[quote] == '\''
}

// Decode only selected outputs. A constant expression must not deserialize
// every previous output, and accessing one step must not copy all other steps.
type codeStepMap struct {
	traits.Mapper
	raw     map[string]string
	mu      sync.Mutex
	decoded map[string]map[string]any
}

func newCodeStepMap(outputs map[string]string) *codeStepMap {
	raw := make(map[string]string, len(outputs))
	keys := make(map[string]any, len(outputs))
	for id, text := range outputs {
		raw[id], keys[id] = text, nil
	}
	return &codeStepMap{Mapper: types.NewStringInterfaceMap(types.DefaultTypeAdapter, keys), raw: raw, decoded: make(map[string]map[string]any)}
}

func (m *codeStepMap) fields(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fields, found := m.decoded[id]; found {
		return fields, true
	}
	text, found := m.raw[id]
	if !found {
		return nil, false
	}
	var output any = text
	var parsed, extra any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&parsed) == nil && decoder.Decode(&extra) == io.EOF {
		output = parsed
	}
	fields := map[string]any{"output": output}
	if object, ok := output.(map[string]any); ok {
		for key, value := range object {
			if key != "output" {
				fields[key] = value
			}
		}
	}
	fields = celInputs(fields)
	m.decoded[id] = fields
	return fields, true
}

func (m *codeStepMap) Find(key ref.Val) (ref.Val, bool) {
	id, ok := key.(types.String)
	if !ok {
		if types.IsUnknownOrError(key) {
			return key, false
		}
		return nil, false
	}
	fields, found := m.fields(string(id))
	if !found {
		return nil, false
	}
	return types.DefaultTypeAdapter.NativeToValue(fields), true
}

func (m *codeStepMap) Get(key ref.Val) ref.Val {
	if value, found := m.Find(key); found || types.IsUnknownOrError(value) {
		return value
	}
	return types.NewErr("no such step: %v", key)
}

func (m *codeStepMap) materialized() traits.Mapper {
	values := make(map[string]any, len(m.raw))
	for id := range m.raw {
		values[id], _ = m.fields(id)
	}
	return types.NewStringInterfaceMap(types.DefaultTypeAdapter, values)
}

func (m *codeStepMap) Value() any                  { return m.materialized().Value() }
func (m *codeStepMap) Equal(other ref.Val) ref.Val { return m.materialized().Equal(other) }
func (m *codeStepMap) ConvertToNative(target reflect.Type) (any, error) {
	return m.materialized().ConvertToNative(target)
}
func (m *codeStepMap) ConvertToType(target ref.Type) ref.Val {
	if target == types.MapType {
		return m
	}
	return m.materialized().ConvertToType(target)
}

func codeContextVariables(render RenderContext) map[string]any {
	run := make(map[string]any, len(render.Env)+1)
	for key, value := range render.Env {
		run[key] = value
	}
	if replay, err := strconv.ParseBool(render.Env["is_replay"]); err == nil {
		run["is_replay"] = replay
	}
	run["metadata"] = render.Metadata
	return map[string]any{
		"steps": newCodeStepMap(render.StepOutputs), "run": run, "env": render.Env,
		"routine": map[string]any{"state": render.State},
		"secrets": render.Secrets, "event": render.Event,
	}
}

// Only fixed namespaces are exposed. In particular, inputs cannot override
// namespaces or obtain arbitrary server environment variables.
func codeVariables(req CodeRunRequest) map[string]any {
	variables := make(map[string]any, len(codeNamespaces))
	for _, name := range codeNamespaces {
		variables[name] = map[string]any{}
		if value, ok := req.Variables[name]; ok && value != nil {
			variables[name] = celValue(value)
		}
	}
	variables["inputs"] = celInputs(req.Inputs)
	return variables
}

// expr deliberately accepts selectors, not general expressions, as operands.
// A resolved value is returned as data and is NEVER scanned for operators.
func codeOperandPath(token string, variables map[string]any) (any, bool, error) {
	rootEnd := strings.IndexAny(token, ".[ ")
	if rootEnd < 0 {
		rootEnd = len(token)
	}
	name := token[:rootEnd]
	if !codeNamespace(name) {
		return nil, false, nil
	}
	value := variables[name]
	for i := rootEnd; i < len(token); {
		var key string
		index := -1
		switch token[i] {
		case '.':
			start := i + 1
			i = start
			for i < len(token) && token[i] != '.' && token[i] != '[' {
				i++
			}
			key = token[start:i]
			if !codePathPart(key) {
				return nil, true, fmt.Errorf("expr: invalid typed selector %q", token)
			}
		case '[':
			i++
			if i < len(token) && token[i] == '"' {
				start := i
				i++
				for i < len(token) && token[i] != '"' {
					if token[i] == '\\' {
						i++
					}
					i++
				}
				if i >= len(token) {
					return nil, true, fmt.Errorf("expr: invalid typed selector %q", token)
				}
				i++
				var err error
				key, err = strconv.Unquote(token[start:i])
				if err != nil {
					return nil, true, fmt.Errorf("expr: invalid typed selector %q", token)
				}
			} else {
				start := i
				for i < len(token) && token[i] >= '0' && token[i] <= '9' {
					i++
				}
				var err error
				index, err = strconv.Atoi(token[start:i])
				if err != nil {
					return nil, true, fmt.Errorf("expr: invalid typed selector %q", token)
				}
			}
			if i >= len(token) || token[i] != ']' {
				return nil, true, fmt.Errorf("expr: invalid typed selector %q", token)
			}
			i++
		default:
			return nil, true, fmt.Errorf("expr: use typed selectors as operands, got %q", token)
		}
		var found bool
		switch object := value.(type) {
		case *codeStepMap:
			if index < 0 {
				value, found = object.fields(key)
			}
		case map[string]any:
			if index < 0 {
				value, found = object[key]
			}
		case map[string]string:
			if index < 0 {
				value, found = object[key]
			}
		case []any:
			if index >= 0 && index < len(object) {
				value, found = object[index], true
			}
		}
		if !found {
			return nil, true, fmt.Errorf("expr: missing typed selector %q", token)
		}
	}
	return value, true, nil
}

func resolveCodeOperand(token string, env map[string]string, variables map[string]any) (string, error) {
	value, typed, err := codeOperandPath(token, variables)
	if err != nil {
		return "", err
	}
	if !typed {
		if value, ok := env[token]; ok {
			return value, nil
		}
		return strings.Trim(token, `"'`), nil
	}
	switch value.(type) {
	case string, json.Number, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(value), nil
	default:
		return "", fmt.Errorf("expr: typed operand %q must be a scalar string, number, or boolean", token)
	}
}
