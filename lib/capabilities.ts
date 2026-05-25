/**
 * Per-membership capability constants — TypeScript mirror of
 * internal/api/capabilities.go.
 *
 * Keep this file in sync with the Go side: adding a capability
 * server-side means appending here too (and adding the
 * corresponding label / icon entry in CAPABILITY_LABELS below so
 * the Members grid renders the new column). Removing a capability
 * is intentionally not supported — keep the constant for
 * backwards-compat with existing rows and stop emitting it from
 * new defaults.
 *
 * A small build-time linter could codegen this from the Go
 * constants; for the MVP we keep it manual and rely on the test
 * suite (capabilities_test.go's TestSlashCommandsCatalog_…) to
 * fail loudly if the catalog references a capability the Go side
 * no longer recognises.
 */

export const Capability = {
  Chat: "chat",
  RoutineCreate: "routine.create",
  SkillCreate: "skill.create",
  CredentialCreate: "credential.create",
  CredentialRotate: "credential.rotate",
  IssueCreate: "issue.create",
  MemoryWrite: "memory.write",
} as const

export type CapabilityValue = (typeof Capability)[keyof typeof Capability]

/** The full ordered list — Members grid column order goes low-stakes
 *  → high-stakes (left to right). Matches how admins think about
 *  delegation risk; see /tmp/wireframes/members-capabilities.html. */
export const ALL_CAPABILITIES: CapabilityValue[] = [
  Capability.Chat,
  Capability.RoutineCreate,
  Capability.IssueCreate,
  Capability.MemoryWrite,
  Capability.SkillCreate,
  Capability.CredentialCreate,
  Capability.CredentialRotate,
]

/** Human-readable labels for the Members grid column headers and
 *  any tooltip/confirm copy. EN + CS so the dashboard can pick by
 *  locale without a translation step. */
export const CAPABILITY_LABELS: Record<
  CapabilityValue,
  { en: string; cs: string; description: string }
> = {
  [Capability.Chat]: {
    en: "Chat",
    cs: "Chat",
    description: "Talk to crew agents. Always implied — every member needs this.",
  },
  [Capability.RoutineCreate]: {
    en: "Create routines",
    cs: "Vytvářet rutiny",
    description: "Schedule recurring pipeline runs.",
  },
  [Capability.IssueCreate]: {
    en: "Create issues",
    cs: "Vytvářet issues",
    description: "File tickets from conversations.",
  },
  [Capability.MemoryWrite]: {
    en: "Write memory",
    cs: "Zapisovat do paměti",
    description: "Persist remembered facts via /remember.",
  },
  [Capability.SkillCreate]: {
    en: "Create skills",
    cs: "Vytvářet skilly",
    description:
      "Generate new SKILL.md authoring instructions for agents. High-stakes — skills run inside agent prompts.",
  },
  [Capability.CredentialCreate]: {
    en: "Create credentials",
    cs: "Vytvářet credentials",
    description: "Add new secrets to the workspace vault. High-stakes.",
  },
  [Capability.CredentialRotate]: {
    en: "Rotate credentials",
    cs: "Rotovat credentials",
    description:
      "Change the value of an existing credential. Separate from create so an oncall user can rotate without vault-add reach.",
  },
} as const

/** Named bundles — match BundleCapabilities in capabilities.go. */
export const CAPABILITY_BUNDLES = {
  chat: [Capability.Chat],
  power: [
    Capability.Chat,
    Capability.RoutineCreate,
    Capability.IssueCreate,
    Capability.MemoryWrite,
  ],
  admin: [
    Capability.Chat,
    Capability.RoutineCreate,
    Capability.SkillCreate,
    Capability.CredentialCreate,
    Capability.CredentialRotate,
    Capability.IssueCreate,
    Capability.MemoryWrite,
  ],
} as const

export type CapabilityBundle = keyof typeof CAPABILITY_BUNDLES

export function hasCapability(
  caps: readonly string[] | undefined | null,
  cap: CapabilityValue,
): boolean {
  // chat is always implied — defensive mirror of HasCapability in
  // Go. Even if a row somehow ends up with no caps, chat is granted
  // because admin can't meaningfully revoke it (revoke = remove
  // the member entirely).
  if (cap === Capability.Chat) return true
  if (!caps) return false
  return caps.includes(cap)
}
