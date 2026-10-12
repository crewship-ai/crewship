import { defineConfig } from 'vitest/config'
import base from './vitest.config'

// Each partition measures the same full source inventory. Only the required
// merged report can evaluate global thresholds; partial runs cannot meet them.
export default defineConfig({
  ...base,
  test: {
    ...base.test,
    coverage: {
      ...base.test?.coverage,
      thresholds: undefined,
      reporter: [],
    },
  },
})
