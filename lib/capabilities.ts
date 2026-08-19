/**
 * Per-membership capability constants — TypeScript mirror of
 * internal/api/capabilities.go.
 *
 * Keep this file in sync with the Go side: adding a capability
 * server-side means appending here too (and adding the
 * corresponding label / icon entry in CAPABILITY_LABELS below so
 * Settings → Members renders the new toggle). Removing a capability
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
  RoutineRun: "routine.run",
  SkillCreate: "skill.create",
  CredentialCreate: "credential.create",
  CredentialRotate: "credential.rotate",
  // Note the colon. It is spelled that way in Go
  // (internal/api/capabilities.go: CapabilityCredentialReveal =
  // "credentials:reveal") and the string is the wire identifier, so it is
  // copied verbatim rather than normalised to match its neighbours.
  CredentialReveal: "credentials:reveal",
  IssueCreate: "issue.create",
  PageCreate: "page.create",
  MemoryWrite: "memory.write",
} as const

export type CapabilityValue = (typeof Capability)[keyof typeof Capability]

/** The full ordered list — low-stakes → high-stakes, which is how admins
 *  think about delegation risk. Settings → Members relies on the order
 *  being stable: the pip summary in a collapsed row puts each capability
 *  at the same x-position in every row, so a single grant stays comparable
 *  down the roster. Inserting in the middle re-sorts that column. */
export const ALL_CAPABILITIES: CapabilityValue[] = [
  Capability.Chat,
  Capability.RoutineCreate,
  // Beside its sibling, and deliberately not appended at the end: the
  // list reads low-stakes → high-stakes, and running a routine sits
  // nowhere near the credential grants. Inserting here does re-sort the
  // pip column once, which is unavoidable for any addition that is not
  // last.
  Capability.RoutineRun,
  Capability.IssueCreate,
  Capability.PageCreate,
  Capability.MemoryWrite,
  Capability.SkillCreate,
  Capability.CredentialCreate,
  Capability.CredentialRotate,
  // Last on purpose: it is the highest-stakes grant in the list — it is the
  // only one that hands a person a plaintext secret — and the grid reads
  // low-stakes → high-stakes left to right.
  Capability.CredentialReveal,
]

/** Human-readable labels for the Settings → Members capability toggles
 *  and any tooltip/confirm copy. The `description` is rendered inline
 *  next to each toggle — it used to hide in a column header's `title`,
 *  which is exactly where nobody looks before handing out
 *  `credentials:reveal`. EN + CS so the dashboard can pick by locale
 *  without a translation step. */
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
  [Capability.RoutineRun]: {
    en: "Run routines",
    cs: "Spouštět rutiny",
    description:
      "Invoke an existing routine, and see it in the slash palette. Separate from creating one: a run executes inside the author crew's container with its credentials and integrations, right now. Not floored by role — a VIEWER granted this can run routines.",
  },
  [Capability.IssueCreate]: {
    en: "Create issues",
    cs: "Vytvářet issues",
    description: "File tickets from conversations.",
  },
  [Capability.PageCreate]: {
    en: "Create pages",
    cs: "Vytvářet stránky",
    description:
      "Author a Page — a workspace-visible surface that names crews as panel owners and routines as producers.",
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
  [Capability.CredentialReveal]: {
    en: "Reveal secrets",
    cs: "Odkrývat hodnoty",
    description:
      "Read a stored secret back in plaintext. Being an OWNER is not sufficient — this is granted per person, deliberately, and never to a whole tier. SEALED credentials stay unreadable to everyone.",
  },
} as const

/** Named bundles — match BundleCapabilities in capabilities.go. */
export const CAPABILITY_BUNDLES = {
  chat: [Capability.Chat],
  power: [
    Capability.Chat,
    Capability.RoutineCreate,
    Capability.RoutineRun,
    Capability.IssueCreate,
    Capability.MemoryWrite,
  ],
  admin: [
    Capability.Chat,
    Capability.RoutineCreate,
    Capability.RoutineRun,
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
