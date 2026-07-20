import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'happy-dom',
    globals: true,
    setupFiles: ['vitest.setup.ts'],
    exclude: ['node_modules', 'e2e', '.claude/worktrees'],
    testTimeout: 30000,
    hookTimeout: 30000,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json', 'html', 'lcov'],
      // Allow-list, not a deny-list. A deny-list silently shrinks the
      // measured surface every time a directory is added; this list says
      // exactly which code the headline number describes.
      //
      // components/ui/** is the first opted-in component slice. The rest of
      // app/ and components/ (~120k lines of React) stays out on purpose:
      // opting it all in at once buys a percentage by way of mock-heavy
      // tests that assert implementation details. Slices get added as real
      // tests land behind them.
      //
      // Note: vitest matches these globs unanchored, so `hooks/**` and
      // `lib/**` also pick up nested `components/features/*/hooks` and
      // `components/features/*/lib` helpers. That is wanted — they are
      // plain logic modules, not JSX.
      include: [
        'lib/**/*.{ts,tsx}',
        'hooks/**/*.{ts,tsx}',
        'app/api/**/*.{ts,tsx}',
        'stores/**/*.{ts,tsx}',
        'components/ui/**/*.{ts,tsx}',
      ],
      exclude: [
        '**/*.d.ts',
        '**/*.config.*',
        '**/mockData/**',
        '**/__tests__/**',
        '**/*.test.{ts,tsx}',
        'lib/logger.ts', // Infrastructure - tested via integration
        'lib/rate-limit.ts', // Infrastructure - tested via integration
        'lib/api/validation.ts', // Middleware - tested via API route tests
        '**/index.ts', // Barrel files
      ],
      // Thresholds are set from a measured run and enforced in CI: the
      // "Test" step of the frontend-test job runs `pnpm test:coverage`,
      // which is `vitest run --coverage`, and vitest exits non-zero when
      // any of these is not met. They sit just under the current numbers
      // so the gate starts green and ratchets upward as coverage lands.
      //
      // Measured on this include set (2026-07-20). Local and CI do NOT agree:
      //   local  statements 68.61  branches 61.40  functions 70.40  lines 69.83
      //   CI     statements 67.46  branches 61.40  functions 70.20  lines 68.48
      // Branches and functions match; statements and lines run ~1.2pp lower on
      // the CI runner. The gate is set from the CI numbers with roughly a point
      // of headroom, because CI is the thing that actually blocks a merge —
      // thresholds derived from a local run fail on the first PR that uses them
      // (which is exactly what happened here). Raise these only after checking
      // a CI run, never a local one.
      thresholds: {
        lines: 67,
        functions: 69,
        branches: 60,
        statements: 66,
      },
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, ''),
    },
  },
})
