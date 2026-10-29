package sidecar

import (
	"crypto/sha256"
	"strings"
	"testing"
)

var usageDecodeSink LLMUsage

func TestUsageBenchmarkFixtures(t *testing.T) {
	for _, codec := range []string{"anthropic", "openai", "google"} {
		want := LLMUsage{Model: "r", InputTokens: 300, OutputTokens: 400, CachedInputTokens: 1200}
		if codec == "anthropic" {
			want = LLMUsage{Model: "r", InputTokens: 40, OutputTokens: 22, CachedInputTokens: 7, CacheCreationTokens: 5}
		} else if codec == "google" {
			want = LLMUsage{Model: "r", InputTokens: 60, OutputTokens: 20, CachedInputTokens: 30}
		}
		for _, scenario := range []string{"mixed", "delta", "nested", "escaped", "malformed-root"} {
			body := usageDecodeBenchmarkStream(codec, scenario)
			expected := want
			if scenario == "mixed" {
				expected.Model = "t"
			} else if scenario == "delta" {
				expected = LLMUsage{}
			}
			if got := g08FrozenParseSSE(codec, body); got != expected {
				t.Fatalf("legacy fixture %s/%s got %+v want %+v", codec, scenario, got, expected)
			}
			if got := parseLLMUsageSSE(codec, body); got != expected {
				t.Fatalf("candidate fixture %s/%s got %+v want %+v", codec, scenario, got, expected)
			}
			t.Logf("corpus %s/%s bytes=%d sha256=%x", codec, scenario, len(body), sha256.Sum256([]byte(body)))
		}
	}
}

// This is a synthetic distribution, not a production traffic profile. Each
// variant consumes identical bytes through the same public parser/observer.
func usageDecodeBenchmarkStream(codec, scenario string) string {
	root, model := `{"model":"r","usage":{"prompt_tokens":1500,"completion_tokens":400,"prompt_tokens_details":{"cached_tokens":1200}}}`, `{"model":"first"}`
	if codec == "anthropic" {
		root = `{"model":"r","usage":{"input_tokens":40,"output_tokens":22,"cache_creation_input_tokens":5,"cache_read_input_tokens":7}}`
	} else if codec == "google" {
		root = `{"modelVersion":"r","usageMetadata":{"promptTokenCount":90,"candidatesTokenCount":20,"cachedContentTokenCount":30}}`
		model = `{"modelVersion":"first"}`
	}
	delta := `{"choices":[{"delta":{"content":"` + strings.Repeat("x", 64) + `"}}]}`
	var stream strings.Builder
	for i := 0; i < 256; i++ {
		body := delta
		switch scenario {
		case "mixed":
			switch i {
			case 0:
				body = model
			case 63:
				body = root
			case 127:
				body = `{"response":` + strings.ReplaceAll(root, `"r"`, `"s"`) + `}`
			case 191:
				body = `{"message":` + strings.ReplaceAll(root, `"r"`, `"t"`) + `}`
			case 255:
				body = "[DONE]"
			}
		case "nested":
			body = root[:len(root)-1] + `,"response":` + root + `,"message":` + root + `}`
		case "escaped":
			if i == 255 {
				body = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(root, "modelVersion", `model\u0056ersion`), `"model"`, `"mo\u0064el"`), "usageMetadata", `usage\u004detadata`), `"usage"`, `"us\u0061ge"`)
			}
		case "malformed-root":
			if codec == "google" {
				body = `{"modelVersion":7,"response":` + root + `}`
			} else {
				body = `{"model":7,"response":` + root + `}`
			}
		case "delta":
		default:
			panic("unknown benchmark scenario")
		}
		stream.WriteString("data: ")
		stream.WriteString(body)
		stream.WriteString("\n\n")
	}
	return stream.String()
}

func BenchmarkUsageDecode(b *testing.B) {
	benchmarkUsageDecode(b, false)
}

// The decision set retains mixed and malformed fallback controls, parser and
// complete observer paths, and every codec without repeating near-identical chunks.
func BenchmarkUsageDecodeDecision(b *testing.B) {
	benchmarkUsageDecode(b, true)
}

func benchmarkUsageDecode(b *testing.B, decision bool) {
	for _, codec := range []string{"anthropic", "openai", "google"} {
		for _, scenario := range []string{"mixed", "delta", "nested", "escaped", "malformed-root"} {
			if decision && scenario != "mixed" && scenario != "malformed-root" {
				continue
			}
			body := usageDecodeBenchmarkStream(codec, scenario)
			want := g08FrozenParseSSE(codec, body)
			name := codec + "/" + scenario
			b.Run("parser/"+name, func(b *testing.B) {
				if got := parseLLMUsageSSE(codec, body); got != want {
					b.Fatalf("fixture differs: %+v != %+v", got, want)
				}
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					usageDecodeSink = parseLLMUsageSSE(codec, body)
				}
			})
			if !decision {
				b.Run("events/"+name, func(b *testing.B) {
					events := strings.SplitAfter(body, "\n\n")
					var scratch []string
					var check LLMUsage
					for _, event := range events {
						mergeLLMUsageMax(&check, parseLLMUsageSSEWithScratch(codec, event, &scratch))
					}
					if check != want {
						b.Fatal("event fixture differs")
					}
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						var result LLMUsage
						for _, event := range events {
							mergeLLMUsageMax(&result, parseLLMUsageSSEWithScratch(codec, event, &scratch))
						}
						usageDecodeSink = result
					}
				})
			}

			for _, size := range []int{64, 4096} {
				if decision && size != 64 {
					continue
				}
				chunkName := "chunk64"
				if size == 4096 {
					chunkName = "chunk4096"
				}
				b.Run("observer/"+name+"/"+chunkName, func(b *testing.B) {
					input := []byte(body)
					var chunks [][]byte
					for offset := 0; offset < len(input); {
						n := min(size, len(input)-offset)
						chunks = append(chunks, input[offset:offset+n])
						offset += n
					}
					check := &sseUsageObserver{codec: codec, limit: maxRequestBodyBytes}
					for _, chunk := range chunks {
						_, _ = check.Write(chunk)
					}
					if check.finish() != want || check.skipped != 0 {
						b.Fatal("observer fixture differs")
					}
					b.SetBytes(int64(len(input)))
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						s := &sseUsageObserver{codec: codec, limit: maxRequestBodyBytes}
						for _, chunk := range chunks {
							_, _ = s.Write(chunk)
						}
						usageDecodeSink = s.finish()
					}
				})
			}
		}
	}
}

func BenchmarkUsageDecodeNonstream(b *testing.B) {
	for _, codec := range []string{"anthropic", "openai", "google"} {
		body := usageDecodeBenchmarkStream(codec, "mixed")
		events := strings.Split(body, "\n\n")
		jsonBody := strings.TrimPrefix(events[63], "data: ")
		b.Run(codec, func(b *testing.B) {
			if parseLLMUsage(codec, jsonBody) != g08FrozenParse(codec, jsonBody) {
				b.Fatal("nonstream fixture differs")
			}
			b.SetBytes(int64(len(jsonBody)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				usageDecodeSink = parseLLMUsage(codec, jsonBody)
			}
		})
	}
}
