export const PROVIDERS = { ANTHROPIC: "Anthropic", OPENAI: "OpenAI", GOOGLE: "Google", CURSOR: "Cursor", FACTORY: "Factory", OPENCODE: "OpenCode Zen", OPENCODE_GO: "OpenCode Go", ZAI_CODING_PLAN: "Z.AI Coding Plan", AWS_BEDROCK: "Amazon Bedrock", OLLAMA: "Ollama" } as const
export const DEFAULT_RUNNER = { ANTHROPIC: "CLAUDE_CODE", OPENAI: "CODEX_CLI", GOOGLE: "GEMINI_CLI", CURSOR: "CURSOR_CLI", FACTORY: "FACTORY_DROID", OPENCODE: "OPENCODE", OPENCODE_GO: "OPENCODE", ZAI_CODING_PLAN: "OPENCODE", AWS_BEDROCK: "OPENCODE", OLLAMA: "OPENCODE" } as const

type Provider = keyof typeof PROVIDERS
type Runner = (typeof DEFAULT_RUNNER)[Provider]

/**
 * The runners that can run a provider's models, its own CLI first. The
 * provider decides the runner, never the other way round: a provider with one
 * entry (Bedrock, the OpenCode plans, Ollama, Cursor, Factory) has its runner
 * set for it, and the picker shows it as fixed.
 */
export const PROVIDER_RUNNERS: Record<Provider, readonly Runner[]> = {
  ANTHROPIC: ["CLAUDE_CODE", "OPENCODE"],
  OPENAI: ["CODEX_CLI", "OPENCODE"],
  GOOGLE: ["GEMINI_CLI", "OPENCODE"],
  CURSOR: ["CURSOR_CLI"],
  FACTORY: ["FACTORY_DROID"],
  OPENCODE: ["OPENCODE"],
  OPENCODE_GO: ["OPENCODE"],
  ZAI_CODING_PLAN: ["OPENCODE"],
  AWS_BEDROCK: ["OPENCODE"],
  OLLAMA: ["OPENCODE"],
}

/** The runner to keep when the provider changes: the current one if it can run the new provider, else that provider's own. */
export function runnerAfterSwitch(provider: Provider, runner: string): Runner {
  return (PROVIDER_RUNNERS[provider] as readonly string[]).includes(runner) ? (runner as Runner) : DEFAULT_RUNNER[provider]
}
