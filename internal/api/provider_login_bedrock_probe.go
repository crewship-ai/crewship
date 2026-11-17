package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

// Only the regional AWS runtime derived from a validated region is dialled.
// No redirects, AWS credential chain, provider error text or secret-bearing URLs.
func probeBedrock(ctx context.Context, value string) testResult {
	a, err := bedrock.Parse(value)
	if err != nil {
		return testResult{Error: err.Error()}
	}
	if a.Expired(time.Now()) {
		return testResult{Error: "Bedrock API key has expired"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{Transport: http.DefaultTransport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	payload := []byte(`{"messages":[{"role":"user","content":[{"text":"Reply OK."}]}],"inferenceConfig":{"maxTokens":1}}`)
	for _, model := range a.Models {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Endpoint()+"/model/"+model+"/converse", bytes.NewReader(payload))
		if err != nil {
			return testResult{Error: "Could not construct Bedrock verification"}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+a.APIKey)
		resp, err := client.Do(req)
		if err != nil {
			return testResult{Error: "Bedrock verification could not reach the regional runtime"}
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		if readErr != nil || len(body) > 65536 {
			return testResult{Status: resp.StatusCode, Error: "Bedrock verification returned an unreadable response"}
		}
		result := testResult{Status: resp.StatusCode}
		switch resp.StatusCode {
		case 200:
			var out struct {
				Output struct {
					Message json.RawMessage `json:"message"`
				} `json:"output"`
			}
			if json.Unmarshal(body, &out) != nil || len(out.Output.Message) == 0 {
				result.Error = "Bedrock returned an invalid Converse response"
				return result
			}
		case 401:
			result.Error = "Bedrock API key is invalid or expired"
			return result
		case 403:
			if strings.Contains(strings.ToLower(string(body)), "agreement") || strings.Contains(strings.ToLower(string(body)), "ftuform") {
				result.Error = "Bedrock model agreement or first-time model access is not enabled"
			} else {
				result.Error = "Bedrock denied access: check IAM permissions and model access"
			}
			return result
		case 404:
			result.Error = "Bedrock model is unavailable in this region"
			return result
		case 429:
			result.Error = "Bedrock model quota or rate limit prevents verification"
			return result
		default:
			result.Error = "Bedrock could not verify the permitted model"
			return result
		}
	}
	return testResult{Valid: true, Status: 200}
}
