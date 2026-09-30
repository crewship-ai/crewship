//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// BrokerAccountingAuthority is mandatory for Responses grants. Implementations
// reserve durably before upstream I/O using current server-resolved authority.
// Settlement must retain the full debit for unknown usage, crashes or failures.
type BrokerAccountingAuthority interface {
	BrokerReserve(context.Context, string, string, string, int64, int64) (string, error)
	BrokerSettle(context.Context, string, string, BrokerUsage) error
}

type BrokerUsage struct {
	Known                                        bool
	InputTokens, OutputTokens, CachedInputTokens int64
}

// terminalResponsesUsage inspects a bounded upstream stream, never agent data.
// It requires one completed event, exact pinned model and unambiguous integer
// usage. Unsupported event/usage shapes conservatively retain the full debit.
func terminalResponsesUsage(raw []byte, model string) BrokerUsage {
	var found BrokerUsage
	count := 0
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	if !bytes.HasSuffix(raw, []byte("\n\n")) {
		return BrokerUsage{}
	}
	frames := bytes.Split(raw, []byte("\n\n"))
	for _, frame := range frames {
		var payload []byte
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				if payload != nil {
					payload = append(payload, '\n')
				}
				payload = append(payload, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
			}
		}
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		v, err := uniqueJSON(decoder, 0)
		if err != nil {
			return BrokerUsage{}
		}
		if _, err = decoder.Token(); err != io.EOF {
			return BrokerUsage{}
		}
		event, ok := v.(map[string]any)
		if !ok {
			return BrokerUsage{}
		}
		if event["type"] == "response.failed" || event["type"] == "response.incomplete" || event["type"] == "error" {
			return BrokerUsage{}
		}
		if event["type"] != "response.completed" {
			continue
		}
		count++
		response, ok := event["response"].(map[string]any)
		if !ok || response["status"] != "completed" || response["model"] != model {
			return BrokerUsage{}
		}
		usage, ok := response["usage"].(map[string]any)
		if !ok {
			return BrokerUsage{}
		}
		input, okIn := usageInteger(usage["input_tokens"])
		output, okOut := usageInteger(usage["output_tokens"])
		total, okTotal := usageInteger(usage["total_tokens"])
		if !okIn || !okOut || !okTotal || input > 1<<30 || output > 1<<30 || total != input+output {
			return BrokerUsage{}
		}
		cached := int64(0)
		if details, exists := usage["input_tokens_details"]; exists {
			m, ok := details.(map[string]any)
			if !ok {
				return BrokerUsage{}
			}
			cached, ok = usageInteger(m["cached_tokens"])
			if !ok || cached > input {
				return BrokerUsage{}
			}
		}
		found = BrokerUsage{Known: true, InputTokens: input, OutputTokens: output, CachedInputTokens: cached}
	}
	if count != 1 {
		return BrokerUsage{}
	}
	return found
}
func usageInteger(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil && i >= 0
}
