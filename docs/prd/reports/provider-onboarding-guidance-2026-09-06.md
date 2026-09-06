# Provider onboarding guidance — 2026-09-06

The Connect step no longer asks the provider a second time. Provider-specific
defaults, key acquisition links and concise instructions are prepared in
`lib/credentials/provider-connection-guides.ts`. This is guidance for the
authentication routes already implemented in Crewship, not new native adapters
or proof of successful live provider execution.

## Official sources inspected

| Provider | Offered connection and important distinction | Official source |
|---|---|---|
| ChatGPT / OpenAI | Device code by default; requires enabling device login in account/workspace settings. Codex auth.json import and API keys remain alternatives. | [Authentication](https://developers.openai.com/codex/auth/) |
| Claude / Anthropic | Existing setup-token subscription route, or a Claude Console API key. | [API overview](https://platform.claude.com/docs/en/api/overview); setup-token route already implemented and tested in the preceding checkpoint |
| Gemini / Google | API key from AI Studio by default, matching documented headless guidance. Existing oauth_creds.json import remains available. Organization Google login may also require a Cloud project, which this wizard does not configure. | [Gemini authentication](https://geminicli.com/docs/get-started/authentication/) |
| Cursor | User API key from Dashboard → API Keys, delivered as CURSOR_API_KEY. Not a BYOK model-provider key. | [CLI authentication](https://cursor.com/docs/cli/reference/authentication) |
| Factory Droid | Factory API key from the Factory settings, delivered as FACTORY_API_KEY. | [Droid CI authentication](https://docs.factory.ai/software-factory/code-review-ci) |
| Grok / xAI | xAI API key, through existing OpenCode integration; no SuperGrok browser login is implemented here. | [xAI quickstart](https://docs.x.ai/developers/quickstart) |
| Groq | Groq Console API key, through existing OpenCode integration. | [Groq quickstart](https://console.groq.com/docs/quickstart) |
| OpenRouter | OpenRouter API key. | [Authentication](https://openrouter.ai/docs/api-reference/authentication) |
| DeepSeek | DeepSeek API platform key, Bearer authentication. | [API authentication](https://api-docs.deepseek.com/api/deepseek-api/) |
| Moonshot / Kimi | Kimi API Platform key; existing mapping is OpenCode `moonshotai`, not a new Kimi Code subscription integration. | [Kimi quickstart](https://platform.kimi.ai/docs/overview) |
| Z.AI | Standard API key. GLM Coding Plan requires a dedicated endpoint; this wizard does not introduce a Coding Plan variant. | [Z.AI quickstart](https://docs.z.ai/guides/overview/quick-start) |
| MiniMax | Standard pay-as-you-go API key, not the separately documented Token Plan Subscription Key. | [MiniMax prerequisites](https://platform.minimax.io/docs/guides/quickstart-preparation) |

The existing OpenCode auth-file mappings were checked against
`internal/orchestrator/opencode_auth_file.go`; [OpenCode provider documentation](https://opencode.ai/docs/providers/)
also documents auth.json and separate provider configurations. No endpoint
or billing-mode changes are introduced by the wizard.

Some old documentation URLs redirect: Cursor's docs.cursor.com auth URL now
lands on the docs index, so the current cursor.com auth page was fetched instead.
Its Markdown version provided the exact Dashboard `/dashboard/api` link.
DeepSeek's root failed to open in the web tool, but its API authentication page
was successfully fetched; the finding is not based only on search snippets.

## UX and safety

- Provider choice is made once. Back permits changing it, clearing the previous
  provider's token. Returning to the same provider preserves the draft.
- Default names follow the provider until manually edited. Owner defaults to
  the signed-in user. Both remain editable through disclosures.
- No decorative brand inference or custom credential fields in provider flow.
- Technical delivery slots remain available under Advanced; existing access
  scope and Keeper/RBAC semantics are not changed.
- Device sign-in has an explicit waiting message, not “Sign-in is empty”.
  Switching to import clears the pending UI gate; completed device identities
  cannot be silently replaced by another sign-in method.
- Import/token and API-key instructions are intentionally short; secrets never
  appear in outbound setup links. Those links open official provider pages.

Live account sign-in, quota, billing and end-to-end agent execution were not
tested for these providers. Browser tests use fake API responses and dummy keys,
including the device-code flow. Remaining PRD native adapters and subscription
endpoint variants are not claimed complete.

## Verification

- `pnpm exec vitest run 'app/(dashboard)/credentials' components/features/credentials lib/credentials lib/credential-providers`: 570 tests / 23 files passed.
- Playwright isolated acceptance: 4 desktop/mobile tests passed. Coverage
  includes the device-code default and import fallback, Gemini's key default
  and Google account alternative, provider switching, automatic account name,
  and the earlier metadata-edit safety cases. Unit tests additionally cover
  every provider's key link and the completed-device Back-navigation guard.
- `go test ./... -count=1 -timeout 30m`: exit 0.
- `go vet ./...`: exit 0.
- `pnpm lint`: no errors, 33 existing warnings.
- `pnpm build`: passed. Deployment rebuild and public checks follow the commit.

Logs: `/tmp/provider-guided-{ui,e2e,go,vet,lint,build}.log`.
