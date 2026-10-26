import { defineConfig } from "vitest/config"
export default defineConfig({ test: { environment: "node", include: ["tools/wordcraft-capabilities/acceptance/*.contract.ts"], maxWorkers: 1 } })
