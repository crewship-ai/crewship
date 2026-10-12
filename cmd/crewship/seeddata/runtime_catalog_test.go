package seeddata

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/crewship-ai/crewship/internal/skills"
)

func TestOptionalCredentialsReadCurrentEnvironment(t *testing.T) {
	t.Setenv("SEED_GITHUB_TOKEN", "")
	require.Nil(t, ResolveGitHubCredential())
	t.Setenv("SEED_GITHUB_TOKEN", "demo-only-github-token")
	github := ResolveGitHubCredential()
	require.NotNil(t, github)
	require.Equal(t, "CLI_TOKEN", github.Type)
	require.Equal(t, "GITHUB", github.Provider)
	require.Equal(t, "GH_TOKEN", github.EnvVarName)
	require.Equal(t, "demo-only-github-token", github.Value)
	for _, tc := range []struct{ name, email, password string }{{"absent", "", ""}, {"email-only", "demo@example.invalid", ""}, {"password-only", "", "demo-only"}, {"complete", "demo@example.invalid", "demo-\"quoted\"\npassword"}} {
		t.Run("google/"+tc.name, func(t *testing.T) {
			t.Setenv("SEED_GOOGLE_EMAIL", tc.email)
			t.Setenv("SEED_GOOGLE_PASSWORD", tc.password)
			credential := ResolveGoogleCredential()
			if tc.email == "" || tc.password == "" {
				require.Nil(t, credential)
				return
			}
			require.NotNil(t, credential)
			require.Equal(t, "SECRET", credential.Type)
			require.Equal(t, "GOOGLE", credential.Provider)
			require.Equal(t, "GOOGLE_API_CREDENTIALS", credential.EnvVarName)
			var decoded map[string]string
			require.NoError(t, json.Unmarshal([]byte(credential.Value), &decoded))
			require.Equal(t, map[string]string{"email": tc.email, "password": tc.password}, decoded)
		})
	}
	for _, enabled := range []string{"", "0", "true", "1"} {
		t.Run("ollama/"+enabled, func(t *testing.T) {
			t.Setenv("CREWSHIP_SEED_OLLAMA", enabled)
			t.Setenv("SEED_OLLAMA_ENDPOINT", "")
			credential := ResolveOllamaEndpointCredential()
			if enabled != "1" {
				require.Nil(t, credential)
				return
			}
			require.NotNil(t, credential)
			require.Equal(t, "ENDPOINT_URL", credential.Type)
			require.Equal(t, "OLLAMA", credential.Provider)
			require.Equal(t, "http://host.docker.internal:11434/v1", credential.Value)
			t.Setenv("SEED_OLLAMA_ENDPOINT", "http://ollama.example.invalid:11434/v1")
			require.Equal(t, "http://ollama.example.invalid:11434/v1", ResolveOllamaEndpointCredential().Value)
		})
	}
}

func TestOAuthCredentialsRequireTokenOrCompleteClientPair(t *testing.T) {
	for _, provider := range []string{"LINEAR", "GOOGLE"} {
		for _, tc := range []struct {
			name, token, id, secret string
			enabled                 bool
		}{
			{"absent", "", "", "", false}, {"id-only", "", "demo-id", "", false},
			{"secret-only", "", "", "demo-secret", false}, {"token-only", "demo-token", "", "", true},
			{"client-pair", "", "demo-id", "demo-secret", true}, {"all", "demo-token", "demo-id", "demo-secret", true},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				for _, p := range []string{"LINEAR", "GOOGLE"} {
					for _, field := range []string{"ACCESS_TOKEN", "CLIENT_ID", "CLIENT_SECRET"} {
						t.Setenv("SEED_"+p+"_OAUTH_"+field, "")
					}
				}
				t.Setenv("SEED_"+provider+"_OAUTH_ACCESS_TOKEN", tc.token)
				t.Setenv("SEED_"+provider+"_OAUTH_CLIENT_ID", tc.id)
				t.Setenv("SEED_"+provider+"_OAUTH_CLIENT_SECRET", tc.secret)
				credentials := ResolveOAuthCredentials()
				if !tc.enabled {
					require.Empty(t, credentials)
					return
				}
				require.Len(t, credentials, 1)
				credential := credentials[0]
				require.Equal(t, tc.token, credential.AccessToken)
				require.Equal(t, tc.id, credential.OAuthClientID)
				require.Equal(t, tc.secret, credential.OAuthClientSecret)
				if provider == "LINEAR" {
					require.Equal(t, "linear", credential.IntegrationName)
					require.Equal(t, "https://api.linear.app/oauth/token", credential.OAuthTokenURL)
					require.Equal(t, "read write", credential.OAuthScopes)
				} else {
					require.Equal(t, "google-workspace", credential.IntegrationName)
					require.Equal(t, "https://oauth2.googleapis.com/token", credential.OAuthTokenURL)
					require.Contains(t, credential.OAuthScopes, "https://www.googleapis.com/auth/calendar")
				}
				t.Setenv("SEED_"+provider+"_OAUTH_ACCESS_TOKEN", "")
				t.Setenv("SEED_"+provider+"_OAUTH_CLIENT_SECRET", "")
				require.Empty(t, ResolveOAuthCredentials(), "credentials must not remain cached after environment is cleared")
			})
		}
	}
}

func TestActiveCatalogsHonorExplicitOptIn(t *testing.T) {
	for _, enabled := range []string{"", "0", "true", "1"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("CREWSHIP_SEED_OLLAMA", enabled)
			agents := ActiveAgents()
			crews := ActiveCrews()
			agentSlugs := map[string]bool{}
			for _, agent := range agents {
				agentSlugs[agent.Slug] = true
			}
			crewSlugs := map[string]bool{}
			for _, crew := range crews {
				crewSlugs[crew.Slug] = true
			}
			require.True(t, agentSlugs["alex"])
			require.True(t, crewSlugs["engineering"])
			require.Equal(t, enabled == "1", agentSlugs["ollie"])
			// Results are independent slices: callers must not corrupt the global catalogue.
			agents[0].Slug = "mutated-result"
			crews[0].Slug = "mutated-result"
			require.NotEqual(t, "mutated-result", ActiveAgents()[0].Slug)
			require.NotEqual(t, "mutated-result", ActiveCrews()[0].Slug)
		})
	}
}

func TestEmbeddedAgentDocumentsAndSkillImports(t *testing.T) {
	for _, agent := range Agents {
		t.Run(agent.Slug, func(t *testing.T) {
			require.NotEmpty(t, strings.TrimSpace(AgentSoul(agent.Slug)))
			require.NotEmpty(t, strings.TrimSpace(AgentPrompt(agent.PromptSlug)))
		})
	}
	require.Empty(t, AgentAskForms(""))
	require.PanicsWithValue(t, "missing soul for agent: no-such-agent", func() { AgentSoul("no-such-agent") })
	require.PanicsWithValue(t, "missing prompt for agent: no-such-agent", func() { AgentPrompt("no-such-agent") })
	require.PanicsWithValue(t, "missing ask forms for: no-such-agent", func() { AgentAskForms("no-such-agent") })
	for _, skill := range Skills {
		t.Run(skill.Slug, func(t *testing.T) {
			parsed, err := skills.ParseSKILLMD(skill.SkillMD())
			require.NoError(t, err)
			require.Equal(t, skills.Slugify(skill.Name), parsed.Meta.Name)
			require.Equal(t, skill.DisplayName, parsed.Meta.DisplayName)
			require.Equal(t, skill.Description, parsed.Meta.Description)
			require.Equal(t, strings.TrimSpace(skill.Content), strings.TrimSpace(parsed.Content))
		})
	}
}

func TestPackLookupAndEmbeddedStoryDelivery(t *testing.T) {
	for _, pack := range Packs {
		found, ok := PackBySlug(pack.Slug)
		require.True(t, ok)
		require.Equal(t, pack, found)
		for _, slug := range []string{pack.ProbeSlug, pack.ReportSlug} {
			if slug == "" {
				continue
			}
			owner, ok := PackForRoutine(slug)
			require.True(t, ok)
			require.Equal(t, pack.Slug, owner.Slug)
		}
	}
	_, ok := PackBySlug("no-such-pack")
	require.False(t, ok)
	_, ok = PackForRoutine("no-such-routine")
	require.False(t, ok)
	_, err := PackFileContent("packs/no-such-file")
	require.True(t, errors.Is(err, fs.ErrNotExist))
	for _, file := range StoryFiles {
		contents, err := StoryFileContent(file.Source)
		require.NoError(t, err)
		require.NotEmpty(t, contents)
		require.True(t, strings.HasPrefix(file.Dest, "shared/demo/business/"))
		if strings.HasSuffix(file.Source, "catalogue.json") {
			var stories []StoryDef
			require.NoError(t, json.Unmarshal(contents, &stories))
			require.Equal(t, Stories, stories)
		}
	}
	_, err = StoryFileContent("stories/business/missing.json")
	require.True(t, errors.Is(err, fs.ErrNotExist))
}

// A broken embedded catalogue must stop seed startup with the missing asset
// named in the diagnostic, instead of silently returning an empty workspace.
// This test is serial; cleanup restores the embed before parallel tests run.
func TestCatalogLoadersFailLoudlyWhenAssetsAreAbsent(t *testing.T) {
	original := builtinFS
	t.Cleanup(func() { builtinFS = original })
	builtinFS = embed.FS{}
	for _, tc := range []struct {
		name string
		load func()
	}{
		{"agents", func() { mustLoadAgents() }},
		{"crews", func() { mustLoadCrews() }},
		{"skills", func() { mustLoadSkills() }},
		{"integrations", func() { mustLoadIntegrations() }},
		{"issues", func() { mustLoadIssuesBundle() }},
		{"pages", func() { mustLoadPages() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				failure := recover()
				require.NotNil(t, failure, "missing built-in data must prevent startup")
				message, ok := failure.(string)
				require.True(t, ok)
				require.Contains(t, message, "seeddata: read builtin/"+tc.name+".yaml:")
			}()
			tc.load()
		})
	}
}

func TestCredentialJSONRejectsUnsupportedValues(t *testing.T) {
	require.PanicsWithValue(t, "seeddata: failed to marshal JSON: json: unsupported type: chan string", func() { marshalJSON(make(chan string)) })
}
