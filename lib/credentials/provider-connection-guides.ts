import type { LOGIN_PROVIDERS } from "./login-providers"

/** Official setup guidance checked 2026-09-06. These describe supported
 * Crewship routes, not every authentication method offered by the vendor. */
export const PROVIDER_CONNECTION_GUIDES = {
  OPENAI: { url: "https://platform.openai.com/api-keys", docs: "https://developers.openai.com/codex/auth/", instruction: "Create a key in your OpenAI API project, then paste it below." },
  ANTHROPIC: { url: "https://platform.claude.com/settings/keys", docs: "https://platform.claude.com/docs/en/api/overview", instruction: "Create a key in the Claude Console, then paste it below." },
  GOOGLE: { url: "https://aistudio.google.com/apikey", docs: "https://geminicli.com/docs/get-started/authentication/", instruction: "Create a Gemini API key in Google AI Studio, then paste it below." },
  CURSOR: { url: "https://cursor.com/dashboard/api", docs: "https://cursor.com/docs/cli/reference/authentication", instruction: "Create a user API key in the Cursor Dashboard, then paste it below. Use a Cursor key, not a model provider key." },
  FACTORY: { url: "https://app.factory.ai/settings/api-keys", docs: "https://docs.factory.ai/software-factory/code-review-ci", instruction: "Create a Factory API key for Droid, then paste it below." },
  XAI: { url: "https://console.x.ai/team/default/api-keys", docs: "https://docs.x.ai/developers/quickstart", instruction: "Create an xAI API key, then paste it below.", note: "Uses Grok through OpenCode. This is not a SuperGrok subscription login." },
  GROQ: { url: "https://console.groq.com/keys", docs: "https://console.groq.com/docs/quickstart", instruction: "Create a key in the Groq Console, then paste it below.", note: "Uses Groq models through OpenCode." },
  OPENROUTER: { url: "https://openrouter.ai/settings/keys", docs: "https://openrouter.ai/docs/api-reference/authentication", instruction: "Create an OpenRouter API key, then paste it below." },
  DEEPSEEK: { url: "https://platform.deepseek.com/api_keys", docs: "https://api-docs.deepseek.com/api/deepseek-api/", instruction: "Create a key in the DeepSeek API platform, then paste it below." },
  MOONSHOT: { url: "https://platform.kimi.ai/console/api-keys", docs: "https://platform.kimi.ai/docs/overview", instruction: "Create a Kimi API Platform key, then paste it below.", note: "Use a platform API key, not a Kimi chat or Kimi Code subscription credential." },
  ZAI: { url: "https://z.ai/manage-apikey/apikey-list", docs: "https://docs.z.ai/guides/overview/quick-start", instruction: "Create a Z.AI API key for the standard API, then paste it below.", note: "GLM Coding Plan requires a different endpoint; this connection uses the standard API." },
  MINIMAX: { url: "https://platform.minimax.io/console/access", docs: "https://platform.minimax.io/docs/guides/quickstart-preparation", instruction: "Create a pay-as-you-go MiniMax API key, then paste it below.", note: "Choose the standard API key, not a Token Plan Subscription Key." },
} satisfies Record<(typeof LOGIN_PROVIDERS)[number]["key"], { url: string; docs: string; instruction: string; note?: string }>

export function providerConnectionGuide(provider: string): { url: string; docs: string; instruction: string; note?: string } | undefined {
  return PROVIDER_CONNECTION_GUIDES[provider.toUpperCase() as keyof typeof PROVIDER_CONNECTION_GUIDES]
}
