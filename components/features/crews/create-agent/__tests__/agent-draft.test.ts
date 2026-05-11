import { describe, it, expect } from "vitest"
import { BUILTIN_PERSONAS } from "@/lib/entities"
import {
  initialAgentDraft,
  applyPersonaDefaults,
  resolveFinalPrompt,
  isIdentityValid,
} from "../types"

describe("agent draft", () => {
  describe("initialAgentDraft", () => {
    it("seeds defaultCrewSlug when provided", () => {
      const d = initialAgentDraft("engineering")
      expect(d.crewSlug).toBe("engineering")
    })

    it("falls back to empty crewSlug when null", () => {
      expect(initialAgentDraft(null).crewSlug).toBe("")
    })

    it("starts with no persona, no custom prompt, no edits", () => {
      const d = initialAgentDraft(null)
      expect(d.selectedPersona).toBeNull()
      expect(d.customPrompt).toBe("")
      expect(d.editedPersonaPrompt).toBeNull()
    })

    it("default leadMode is 'active' (used only when role=LEAD)", () => {
      expect(initialAgentDraft(null).leadMode).toBe("active")
    })

    it("default timeoutSeconds matches backend default (1800)", () => {
      expect(initialAgentDraft(null).timeoutSeconds).toBe(1800)
    })
  })

  describe("isIdentityValid", () => {
    it("rejects empty name", () => {
      const d = initialAgentDraft("eng")
      expect(isIdentityValid(d)).toBe(false)
    })

    it("rejects single-character name", () => {
      const d = { ...initialAgentDraft("eng"), name: "X", slug: "x" }
      expect(isIdentityValid(d)).toBe(false)
    })

    it("requires valid slug format (lowercase, digits, hyphens, 2+ chars)", () => {
      const base = initialAgentDraft("eng")
      expect(isIdentityValid({ ...base, name: "Test", slug: "Test" })).toBe(false) // uppercase
      expect(isIdentityValid({ ...base, name: "Test", slug: "x" })).toBe(false) // too short
      expect(isIdentityValid({ ...base, name: "Test", slug: "te_st" })).toBe(false) // underscore
      expect(isIdentityValid({ ...base, name: "Test", slug: "test" })).toBe(true)
      expect(isIdentityValid({ ...base, name: "Test", slug: "te-st-2" })).toBe(true)
    })

    it("requires crewSlug for AGENT and LEAD", () => {
      const base = { ...initialAgentDraft(null), name: "Test", slug: "test" }
      expect(isIdentityValid({ ...base, agentRole: "AGENT" })).toBe(false)
      expect(isIdentityValid({ ...base, agentRole: "LEAD" })).toBe(false)
      expect(isIdentityValid({ ...base, agentRole: "AGENT", crewSlug: "eng" })).toBe(true)
    })

    it("rejects slugs with invalid characters (dots, spaces, slashes)", () => {
      const base = { ...initialAgentDraft("eng"), name: "Test" }
      expect(isIdentityValid({ ...base, slug: "te.st" })).toBe(false)
      expect(isIdentityValid({ ...base, slug: "te st" })).toBe(false)
      expect(isIdentityValid({ ...base, slug: "te/st" })).toBe(false)
    })

    it("rejects whitespace-only name", () => {
      const d = { ...initialAgentDraft("eng"), name: "  ", slug: "test" }
      expect(isIdentityValid(d)).toBe(false)
    })
  })

  describe("applyPersonaDefaults", () => {
    const filip = BUILTIN_PERSONAS.find((p) => p.id === "b_filip")!

    it("copies LLM, profile, memory, timeout from persona", () => {
      const before = initialAgentDraft("research")
      const after = applyPersonaDefaults(before, filip)
      expect(after.selectedPersona).toBe(filip)
      expect(after.llmProvider).toBe(filip.llmProvider)
      expect(after.llmModel).toBe(filip.llmModel)
      expect(after.cliAdapter).toBe(filip.cliAdapter)
      expect(after.toolProfile).toBe(filip.toolProfile)
      expect(after.memoryEnabled).toBe(filip.memoryEnabled)
      expect(after.timeoutSeconds).toBe(filip.timeoutSeconds)
      expect(after.avatarStyle).toBe(filip.avatarStyle)
    })

    it("inherits roleTitle when user hasn't typed one", () => {
      const before = initialAgentDraft("research")
      const after = applyPersonaDefaults(before, filip)
      expect(after.roleTitle).toBe(filip.roleTitle)
    })

    it("preserves user-typed roleTitle", () => {
      const before = { ...initialAgentDraft("research"), roleTitle: "Custom Title" }
      const after = applyPersonaDefaults(before, filip)
      expect(after.roleTitle).toBe("Custom Title")
    })

    it("does NOT touch identity fields (name, slug, crewSlug)", () => {
      const before = { ...initialAgentDraft("research"), name: "Bob", slug: "bob" }
      const after = applyPersonaDefaults(before, filip)
      expect(after.name).toBe("Bob")
      expect(after.slug).toBe("bob")
      expect(after.crewSlug).toBe("research")
    })

    it("clears any prior edited prompt and customPrompt", () => {
      const before = {
        ...initialAgentDraft("eng"),
        editedPersonaPrompt: "old edit",
        customPrompt: "stale custom",
      }
      const after = applyPersonaDefaults(before, filip)
      expect(after.editedPersonaPrompt).toBeNull()
      expect(after.customPrompt).toBe("")
    })

    describe("avatar — touched flag survives persona pick", () => {
      it("inherits persona avatar style when user has NOT touched the picker", () => {
        const before = initialAgentDraft("research")
        expect(before.avatarTouched).toBe(false)
        const after = applyPersonaDefaults(before, filip)
        expect(after.avatarStyle).toBe(filip.avatarStyle)
      })

      it("preserves user's avatar style when avatarTouched=true", () => {
        const before = {
          ...initialAgentDraft("research"),
          avatarStyle: "lorelei",
          avatarSeed: "custom-seed-xyz",
          avatarTouched: true,
        }
        const after = applyPersonaDefaults(before, filip)
        expect(after.avatarStyle).toBe("lorelei")
        expect(after.avatarSeed).toBe("custom-seed-xyz")
      })

      it("avatarTouched flag itself is preserved across persona picks", () => {
        const before = { ...initialAgentDraft("eng"), avatarTouched: true }
        const after = applyPersonaDefaults(before, filip)
        expect(after.avatarTouched).toBe(true)
      })
    })
  })

  describe("resolveFinalPrompt", () => {
    const filip = BUILTIN_PERSONAS.find((p) => p.id === "b_filip")!

    it("prefers customPrompt when non-empty", () => {
      const d = applyPersonaDefaults(initialAgentDraft("eng"), filip)
      expect(resolveFinalPrompt({ ...d, customPrompt: "  Hello custom  " })).toBe("Hello custom")
    })

    it("uses editedPersonaPrompt when set and customPrompt empty", () => {
      const d = applyPersonaDefaults(initialAgentDraft("eng"), filip)
      expect(resolveFinalPrompt({ ...d, editedPersonaPrompt: "edited version" })).toBe(
        "edited version",
      )
    })

    it("falls back to persona.systemPrompt", () => {
      const d = applyPersonaDefaults(initialAgentDraft("eng"), filip)
      expect(resolveFinalPrompt(d)).toBe(filip.systemPrompt)
    })

    it("returns empty string when nothing is set (Blank path)", () => {
      expect(resolveFinalPrompt(initialAgentDraft("eng"))).toBe("")
    })

    it("customPrompt wins even when editedPersonaPrompt is also set", () => {
      const d = applyPersonaDefaults(initialAgentDraft("eng"), filip)
      const both = { ...d, customPrompt: "custom", editedPersonaPrompt: "edited" }
      expect(resolveFinalPrompt(both)).toBe("custom")
    })
  })

  describe("BUILTIN_PERSONAS data integrity", () => {
    it("has 12 entries", () => {
      expect(BUILTIN_PERSONAS).toHaveLength(12)
    })

    it("every entry has unique id starting with b_", () => {
      const ids = BUILTIN_PERSONAS.map((p) => p.id)
      expect(new Set(ids).size).toBe(ids.length)
      for (const id of ids) expect(id).toMatch(/^b_/)
    })

    it("every entry has a non-trivial system prompt", () => {
      for (const p of BUILTIN_PERSONAS) {
        expect(p.systemPrompt.length).toBeGreaterThan(100)
        expect(p.systemPrompt).toMatch(/PERSONALITY|RESPONSIBILITIES/)
      }
    })

    it("every entry has a DiceBear avatarStyle (not emoji or icon name)", () => {
      const validStyles = new Set([
        "bottts-neutral", "adventurer", "fun-emoji", "pixel-art",
        "micah", "notionists", "thumbs", "lorelei", "big-smile", "avataaars",
      ])
      for (const p of BUILTIN_PERSONAS) {
        expect(validStyles.has(p.avatarStyle)).toBe(true)
      }
    })

    it("every entry has a valid LLM provider + non-empty model", () => {
      for (const p of BUILTIN_PERSONAS) {
        // Multi-CLI wave added CURSOR + FACTORY providers; personas are
        // intentionally diversified across all 6 adapters so the catalog
        // demos multi-CLI capability instead of being all-Anthropic.
        expect(["ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY", "OLLAMA"]).toContain(p.llmProvider)
        expect(p.llmModel.length).toBeGreaterThan(0)
      }
    })

    it("every entry has a CLI adapter from the canonical enum", () => {
      const validAdapters = new Set(["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"])
      for (const p of BUILTIN_PERSONAS) {
        expect(validAdapters.has(p.cliAdapter)).toBe(true)
      }
    })

    it("BUILTIN_PERSONAS demonstrate multi-CLI by using at least 4 different adapters", () => {
      // Pre-multi-CLI wave: every persona was CLAUDE_CODE — catalog gave
      // zero hint that other CLIs were supported. After diversification we
      // showcase Codex/Cursor/Gemini/Droid/OpenCode alongside Claude Code.
      const adapters = new Set(BUILTIN_PERSONAS.map((p) => p.cliAdapter))
      expect(adapters.size).toBeGreaterThanOrEqual(4)
    })

    it("every entry has a tool profile from the canonical enum", () => {
      const validProfiles = new Set(["MINIMAL", "CODING", "FULL"])
      for (const p of BUILTIN_PERSONAS) {
        expect(validProfiles.has(p.toolProfile)).toBe(true)
      }
    })

    it("every LEAD persona has agentRole=LEAD", () => {
      for (const p of BUILTIN_PERSONAS) {
        if (
          p.roleTitle.toLowerCase().includes("lead") ||
          p.roleTitle.toLowerCase().includes("director")
        ) {
          expect(p.agentRole).toBe("LEAD")
        }
      }
    })

    it("every entry has a defaultCrewSlug matching one of the seed crews", () => {
      const validCrews = new Set(["engineering", "research", "quality", "devops"])
      for (const p of BUILTIN_PERSONAS) {
        expect(validCrews.has(p.defaultCrewSlug)).toBe(true)
      }
    })

    it("every suggestedSlug is a valid DiceBear seed (lowercase, digits, hyphens)", () => {
      for (const p of BUILTIN_PERSONAS) {
        expect(p.suggestedSlug).toMatch(/^[a-z0-9-]{2,}$/)
      }
    })

    it("every entry has timeoutSeconds within sane bounds (60s — 2h)", () => {
      for (const p of BUILTIN_PERSONAS) {
        expect(p.timeoutSeconds).toBeGreaterThanOrEqual(60)
        expect(p.timeoutSeconds).toBeLessThanOrEqual(7200)
      }
    })
  })

  describe("submit body — full parity with POST /api/v1/agents", () => {
    // Drift detector. When this list diverges from createAgentRequest's JSON
    // tags in internal/api/agents_create.go, the wizard is sending fewer
    // fields than the backend accepts (or vice versa) — fail loudly.
    const filip = BUILTIN_PERSONAS.find((p) => p.id === "b_filip")!

    it("a fully-filled draft produces all 16 keys the backend handler reads", () => {
      const draft = applyPersonaDefaults(
        {
          ...initialAgentDraft("research"),
          name: "Filip Test",
          slug: "filip-test",
          roleTitle: "QA",
          agentRole: "LEAD", // exercise leadMode path
        },
        filip,
      )

      // Mirror the body construction in create-agent-dialog.tsx submit().
      const body: Record<string, unknown> = {
        name: draft.name.trim(),
        slug: draft.slug.trim(),
        agent_role: draft.agentRole,
        crew_id: null,
        description: draft.description.trim() || null,
        role_title: draft.roleTitle.trim() || null,
        lead_mode: draft.agentRole === "LEAD" ? draft.leadMode : null,
        cli_adapter: draft.cliAdapter,
        llm_provider: draft.llmProvider,
        llm_model: draft.llmModel,
        system_prompt: resolveFinalPrompt(draft) || null,
        avatar_seed: draft.avatarSeed.trim() || null,
        avatar_style: draft.avatarStyle,
        timeout_seconds: draft.timeoutSeconds,
        tool_profile: draft.toolProfile,
        memory_enabled: draft.memoryEnabled,
      }

      expect(Object.keys(body).sort()).toEqual([
        "agent_role",
        "avatar_seed",
        "avatar_style",
        "cli_adapter",
        "crew_id",
        "description",
        "lead_mode",
        "llm_model",
        "llm_provider",
        "memory_enabled",
        "name",
        "role_title",
        "slug",
        "system_prompt",
        "timeout_seconds",
        "tool_profile",
      ])
    })

    it("lead_mode is null for non-LEAD roles (backend ignores otherwise)", () => {
      const draft = { ...initialAgentDraft("eng"), agentRole: "AGENT" as const }
      const lead_mode = draft.agentRole === "LEAD" ? draft.leadMode : null
      expect(lead_mode).toBeNull()
    })

    it("trims user-typed name and slug before sending", () => {
      const d = { ...initialAgentDraft("eng"), name: "  Bob  ", slug: " bob " }
      expect(d.name.trim()).toBe("Bob")
      expect(d.slug.trim()).toBe("bob")
    })

    it("converts empty optional fields to null (not '') for the API", () => {
      const d = initialAgentDraft("eng")
      expect(d.description.trim() || null).toBeNull()
      expect(d.roleTitle.trim() || null).toBeNull()
      expect(d.avatarSeed.trim() || null).toBeNull()
      expect(resolveFinalPrompt(d) || null).toBeNull()
    })
  })
})
