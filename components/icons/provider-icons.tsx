import { Bot, Sparkles, Cpu, Code } from "lucide-react"
import type { SVGProps } from "react"

type IconProps = SVGProps<SVGSVGElement>

export const AnthropicIcon = Bot
export const OpenAIIcon = Sparkles
export const GeminiIcon = Cpu
export const OpenCodeIcon = Code

export const PROVIDER_ICONS: Record<string, React.ComponentType<IconProps>> = {
  ANTHROPIC: Bot,
  OPENAI: Sparkles,
  GOOGLE: Cpu,
}
