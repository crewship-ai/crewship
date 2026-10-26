import { defineConfig } from "vitest/config"
export default defineConfig({ test: { environment: "node", include: ["e2e/wordcraft-ce/*.test.ts"], maxWorkers: 1 } })
