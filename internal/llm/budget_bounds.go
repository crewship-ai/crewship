package llm

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/modelcatalog"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

// Only concrete host-configured official metered codecs can prove bounds.
// Compatibility endpoints and subscription transports are deliberately denied
// under hard USD caps; their model names alone are not a pricing contract.
func providerBudgetBounds(provider Provider, request Request) *paymaster.CallBounds {
	var output int
	switch p := provider.(type) {
	case *OpenAI:
		if p.name() != "openai" || p.chatURL() != openaiAPIURL || p.cfg.NoAuth || !strings.HasPrefix(p.apiKey, "sk-") || len(p.cfg.Headers) > 0 || len(p.cfg.ExtraBody) > 0 || (p.cfg.AuthHeader != "" && p.cfg.AuthHeader != "Authorization") || (p.cfg.AuthPrefix != "" && p.cfg.AuthPrefix != "Bearer ") || (p.maxTokensField() != "max_tokens" && p.maxTokensField() != "max_completion_tokens") {
			return nil
		}
		output = request.MaxTokens
		if output <= 0 {
			output = p.cfg.DefaultMaxTokens
		}
	case *Anthropic:
		if p.name() != "anthropic" || p.chatURL(request.Model, false) != anthropicAPIURL || !strings.HasPrefix(p.apiKey, "sk-ant-") || p.cfg.Sign != nil || (p.cfg.Version != "" && p.cfg.Version != anthropicDefaultVersion) || (p.cfg.Beta != nil && !(len(p.cfg.Beta) == 0 || len(p.cfg.Beta) == 1 && p.cfg.Beta[0] == anthropicDefaultBeta)) || p.cfg.ModelInPath || p.cfg.VersionInBody {
			return nil
		}
		output = request.MaxTokens
		if output == 0 {
			output = 4096
		}
	default:
		return nil
	}
	model, ok := modelcatalog.Default().Lookup(provider.Name(), request.Model)
	if !ok || model.Limit.Context < 1 || model.Limit.Context > 2<<20 || model.Limit.Output < 1 || model.Limit.Output > 2<<20 {
		return nil
	}
	bound := model.Limit.Output
	if model.Reasoning {
		if p, ok := provider.(*OpenAI); ok && p.maxTokensField() != "max_completion_tokens" {
			output = 0
		}
	}
	if output > 0 && int64(output) < bound {
		bound = int64(output)
	}
	return &paymaster.CallBounds{MaxInputTokens: model.Limit.Context, MaxOutputTokens: bound}
}
