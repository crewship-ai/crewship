/** Only providers accepted by the provider-login API; not the decorative brand catalog. */
export const LOGIN_PROVIDERS = [
  { key: "OPENAI", label: "ChatGPT / OpenAI", detail: "Codex sign-in or API key", subscription: true },
  { key: "ANTHROPIC", label: "Claude / Anthropic", detail: "Claude setup token or API key", subscription: true },
  { key: "GOOGLE", label: "Gemini / Google", detail: "Gemini login file or API key", subscription: true },
  { key: "XAI", label: "Grok / xAI", detail: "API key · via OpenCode", subscription: false },
  { key: "CURSOR", label: "Cursor", detail: "Cursor API key", subscription: false },
  { key: "FACTORY", label: "Factory Droid", detail: "Factory API key", subscription: false },
  { key: "GROQ", label: "Groq", detail: "API key · via OpenCode", subscription: false },
  { key: "OPENROUTER", label: "OpenRouter", detail: "OpenRouter API key", subscription: false },
  { key: "DEEPSEEK", label: "DeepSeek", detail: "DeepSeek API key", subscription: false },
  { key: "MOONSHOT", label: "Moonshot / Kimi", detail: "Moonshot API key", subscription: false },
  { key: "ZAI", label: "Z.AI", detail: "Z.AI API key", subscription: false },
  { key: "MINIMAX", label: "MiniMax", detail: "MiniMax API key", subscription: false },
] as const

export function loginProvider(key: string) {
  return LOGIN_PROVIDERS.find((p) => p.key === key.toUpperCase())
}
