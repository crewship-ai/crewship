import globals from "globals";
import typescriptEslint from "@typescript-eslint/eslint-plugin";
import tsParser from "@typescript-eslint/parser";
import reactHooksPlugin from "eslint-plugin-react-hooks";

// eslint-plugin-react was previously loaded only to turn off two defaults
// (react-in-jsx-scope, prop-types). It's effectively a no-op here, and as of
// 7.37 still hasn't declared eslint ^10 in its peerDeps, so dropping it is the
// cleanest way through the eslint 10 bump.

// Same-origin /api/* fetch guard — see the long note on the main config's
// rules below. Extracted so the palette-token config (which re-declares
// no-restricted-syntax for components/app) can carry it forward instead of
// silently dropping it.
const fetchRestrictions = [
  {
    selector:
      "CallExpression[callee.name='fetch'] > Literal.arguments[value=/^\\/api\\//]",
    message:
      "Route same-origin /api/* calls through apiFetch (@/lib/api-fetch) instead of bare fetch(). If this must stay on raw fetch (auth flow / streaming), add `// eslint-disable-next-line no-restricted-syntax -- <reason>`.",
  },
  {
    selector:
      "CallExpression[callee.name='fetch'] > TemplateLiteral.arguments > TemplateElement.quasis:first-child[value.raw=/^\\/api\\//]",
    message:
      "Route same-origin /api/* calls through apiFetch (@/lib/api-fetch) instead of bare fetch(). If this must stay on raw fetch (auth flow / streaming), add `// eslint-disable-next-line no-restricted-syntax -- <reason>`.",
  },
  {
    selector:
      "CallExpression[callee.property.name='fetch'] > Literal.arguments[value=/^\\/api\\//]",
    message:
      "Route same-origin /api/* calls through apiFetch (@/lib/api-fetch) instead of bare fetch(). If this must stay on raw fetch (auth flow / streaming), add `// eslint-disable-next-line no-restricted-syntax -- <reason>`.",
  },
];

// Raw Tailwind palette colours and bare hex are fixed across themes — exactly
// the bug the semantic tokens (success/warn/destructive/info/notice/purple/
// gold + surface tokens) exist to fix. Ban them in components/app so the next
// feature can't reintroduce text-emerald-400. See
// .claude/context/prd/BRIEF-COLOR-TOKENS-2026.md.
const paletteTokenMessage =
  "Use a semantic token (success/warn/destructive/info/notice/purple/gold) or a surface token (card/muted/accent/border/muted-foreground/surface-*). Raw palette colours don't follow the theme. See .claude/context/prd/BRIEF-COLOR-TOKENS-2026.md";
const paletteRegex =
  "/\\b(bg|text|border|ring|fill|stroke|from|to|via|divide|outline|decoration|caret|placeholder)-(emerald|green|red|rose|amber|yellow|orange|blue|sky|cyan|teal|violet|purple|fuchsia|zinc|slate|gray|neutral|stone|pink|lime|indigo)-[0-9]{2,3}\\b/";
const paletteRestrictions = [
  { selector: `Literal[value=${paletteRegex}]`, message: paletteTokenMessage },
  { selector: `TemplateElement[value.raw=${paletteRegex}]`, message: paletteTokenMessage },
];

// §2 exceptions: files that legitimately keep raw palette classes because the
// colour encodes identity/category (per-crew/avatar/label/domain/source
// colours, provider brand tints, node-kind identity, data-viz intensity
// scales, syntax-highlight tokens) or is an indigo "AI-agent" marker with no
// token, or is one of two genuinely-ambiguous deferrals. Kept literal on
// purpose; see the brief §2.
const PALETTE_ALLOWLIST = [
  "components/admin/backup-list.tsx",
  "components/features/activity/overview-nodes.tsx",
  "components/features/activity/sub-span-visual.tsx",
  "components/features/activity/trace-step-node.tsx",
  "components/features/chat/chat-tree-row.tsx",
  "components/features/crews/__tests__/crew-icon-picker-dialog.test.tsx",
  "components/features/crews/bottom-panel/files-tab.tsx",
  "components/features/crews/bottom-panel/runs-tab.tsx",
  "components/features/crews/bottom-panel/yaml-tab.tsx",
  "components/features/crews/create-crew/step-runtime.tsx",
  "components/features/crews/model-library-picker.tsx",
  "components/features/dashboard/agent-heatmap.tsx",
  "components/features/inbox/inbox-list.tsx",
  "components/features/integrations/composio/agent-access-tab.tsx",
  "components/features/integrations/composio/shared.tsx",
  "components/features/integrations/trust-tier-badge.tsx",
  "components/features/issues/label-badge.tsx",
  "components/features/issues/tiptap-editor-toolbar.tsx",
  "components/features/journal/journal-entry-card.tsx",
  "components/features/journal/runs-view.tsx",
  "components/features/orchestration/context-detail-panel.tsx",
  "components/features/orchestration/issue-detail-inline.tsx",
  "components/features/routines/routine-dry-run-report.tsx",
  "components/features/routines/routine-flow-diagram.tsx",
  "components/features/routines/routine-mini-trace.tsx",
  "components/features/routines/routine-overview-tab.tsx",
  "components/features/routines/routine-readable-summary.tsx",
  "components/features/routines/routine-touches.tsx",
  "components/features/settings/sections/general-section.tsx",
  "components/features/skills/skill-card.tsx",
  "components/features/skills/skills-browser.tsx",
  "components/features/skills/skills-detail-panel.tsx",
  "components/skills/skill-detail.tsx",
];

export default [
  {
    files: ["**/*.{js,mjs,cjs,ts,jsx,tsx}"],
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
        ...globals.es2021,
      },
      parser: tsParser,
      parserOptions: {
        ecmaVersion: "latest",
        sourceType: "module",
        ecmaFeatures: {
          jsx: true,
        },
      },
    },
    plugins: {
      "@typescript-eslint": typescriptEslint,
      "react-hooks": reactHooksPlugin,
    },
    rules: {
      // TypeScript rules (relaxed for gradual adoption)
      "@typescript-eslint/no-unused-vars": ["warn", {
        argsIgnorePattern: "^_",
        varsIgnorePattern: "^_",
        caughtErrorsIgnorePattern: "^_"
      }],
      "@typescript-eslint/no-explicit-any": "warn",

      // React Hooks rules
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",

      // Force same-origin /api/* calls through apiFetch (lib/api-fetch.ts):
      // single-flight 401->refresh, cross-tab session-expired broadcast, and
      // the same-origin/CSRF guard live there. A bare `fetch("/api/...")`
      // silently bypasses all of it. New bypasses are caught here.
      //
      // Legitimate exceptions (auth/pre-session flows where a 401 is
      // meaningful rather than "session expired", and request/response
      // streaming that the refresh-retry would break) must opt out with an
      // explicit `// eslint-disable-next-line no-restricted-syntax -- <reason>`
      // on the offending fetch call.
      "no-restricted-syntax": ["error", ...fetchRestrictions],
    },
  },
  {
    // Palette-token guardrail: ban raw Tailwind palette classes in the UI so
    // the theme tokens can't decay back to theme-blind colours. Re-declares
    // no-restricted-syntax for these files, so it re-includes fetchRestrictions
    // to keep the /api fetch guard alive here too.
    files: ["components/**/*.tsx", "app/**/*.tsx"],
    ignores: ["lib/colors.ts", "lib/crew-icons.ts", ...PALETTE_ALLOWLIST],
    rules: {
      "no-restricted-syntax": ["error", ...fetchRestrictions, ...paletteRestrictions],
    },
  },
  {
    ignores: [
      ".next/**",
      "out/**",
      "web/out/**",
      "node_modules/**",
      "dist/**",
      "build/**",
      "coverage/**",
      "*.config.{js,ts}",
      "public/**",
      "lib/generated/**",
      ".claude/**",
      "e2e/**",
    ],
  },
];
