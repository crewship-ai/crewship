import { defineConfig } from "vitest/config"
import base from "./vitest.config"

// Explicit whole-frontend audit. Keep its denominator separate from the
// historical shared-module report until the application-wide gate is met.
export default defineConfig({
  ...base,
  test: {
    ...base.test,
    coverage: {
      ...base.test?.coverage,
      include: [
        "app/**/*.{ts,tsx}",
        "components/**/*.{ts,tsx}",
        "hooks/**/*.{ts,tsx}",
        "lib/**/*.{ts,tsx}",
        "stores/**/*.{ts,tsx}",
      ],
      exclude: [
        "**/*.d.ts",
        "**/*.config.*",
        "**/mockData/**",
        "**/__tests__/**",
        "**/*.test.{ts,tsx}",
        "lib/generated/**", // Generated Prisma client, not handwritten product logic.
      ],
      reportsDirectory: "coverage/full-frontend",
      thresholds: { perFile: true, lines: 90, branches: 90 },
    },
  },
})
