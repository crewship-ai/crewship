import type { MCPTemplate } from "../../types"

export const stripe: MCPTemplate = {
  name: "stripe",
  label: "Stripe",
  icon: "credit-card",
  transport: "stdio",
  command: "npx",
  args: "-y @stripe/mcp",
  envHint: "STRIPE_SECRET_KEY",
}
