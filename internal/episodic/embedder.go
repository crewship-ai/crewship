package episodic

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Embedder turns text into a fixed-dimension float32 vector. The
// production implementation talks to a local Ollama server running the
// nomic-embed-text model, but tests inject a deterministic stub so no
// network is required.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Dim() int
	Model() string
}

// OllamaEmbedder calls Ollama's /api/embeddings HTTP endpoint. BaseURL
// defaults to http://localhost:11434 but is configurable so Crewship
// remote dev (OLLAMA_MODELS on a VM) can point at the right host.
type OllamaEmbedder struct {
	BaseURL    string
	ModelName  string
	HTTPClient *http.Client
	dim        int
}

// NewOllamaEmbedder returns an embedder pointed at the given base URL.
// The default nomic-embed-text model produces 768-dimensional vectors.
// Dim is lazy-probed on first Embed and cached so subsequent calls don't
// pay the round-trip cost.
func NewOllamaEmbedder(baseURL string) *OllamaEmbedder {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaEmbedder{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		ModelName:  "nomic-embed-text",
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Embed sends text to Ollama and returns the vector. We cap input length
// at 4096 bytes because nomic-embed-text's context window is 2K tokens
// and longer inputs silently truncate on the server with no indication.
// The cut happens on a rune boundary so multi-byte UTF-8 (Czech, CJK,
// emoji) doesn't hand the embedder a U+FFFD-poisoned prompt.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if len(text) > 4096 {
		text = truncateAtRune(text, 4096)
	}
	body, _ := json.Marshal(map[string]any{
		"model":  e.ModelName,
		"prompt": text,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("episodic: embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("episodic: ollama unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slurp, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("episodic: ollama http %d: %s", resp.StatusCode, strings.TrimSpace(string(slurp)))
	}

	var out struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("episodic: decode ollama response: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("episodic: ollama returned empty embedding")
	}
	vec := make([]float32, len(out.Embedding))
	for i, v := range out.Embedding {
		vec[i] = float32(v)
	}
	e.dim = len(vec)
	return vec, nil
}

func (e *OllamaEmbedder) Dim() int {
	if e.dim == 0 {
		return 768 // nomic-embed-text default; refined on first call
	}
	return e.dim
}

func (e *OllamaEmbedder) Model() string {
	return e.ModelName
}

// truncateAtRune returns at most maxBytes bytes of s, walking back from
// maxBytes to the most recent rune boundary so a multi-byte UTF-8 rune
// is never split. utf8.RuneStart catches the start byte; up to 3 bytes
// of walkback covers any valid 1–4-byte sequence.
func truncateAtRune(s string, maxBytes int) string {
	if maxBytes <= 0 || maxBytes >= len(s) {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// EncodeVector packs a float32 slice into little-endian bytes for BLOB
// storage in SQLite. Matching DecodeVector below reverses it. The choice
// of little-endian is deliberate — every platform Crewship runs on is
// little-endian, and avoiding endian negotiation simplifies the hot path.
func EncodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:(i+1)*4], math.Float32bits(x))
	}
	return buf
}

// DecodeVector reverses EncodeVector. Length is checked and an error is
// returned for a malformed blob rather than panicking.
func DecodeVector(blob []byte, dim int) ([]float32, error) {
	if len(blob) != 4*dim {
		return nil, fmt.Errorf("episodic: vector blob length %d doesn't match dim %d", len(blob), dim)
	}
	v := make([]float32, dim)
	for i := 0; i < dim; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4 : (i+1)*4]))
	}
	return v, nil
}

// cosine returns the cosine similarity of a and b in [-1,1]. Returns 0
// for zero-norm vectors to avoid NaN poisoning downstream sort.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
