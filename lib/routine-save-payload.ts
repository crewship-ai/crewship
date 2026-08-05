// Bodies for POST /pipelines/save.
//
// Pure, and in lib, because getting this wrong is invisible until a
// server rejects it. The first version of the routine kebab posted
// `{definition, skip_test_gate}` and nothing else: the endpoint requires
// `slug` and `name`, so every rename 400'd — and the one save that did
// land re-titled the routine with its slug, because the page reads its
// heading from the stored `name` COLUMN, not from the definition.
//
// Both mistakes are the same one: assuming the definition is the whole
// payload when the row has fields of its own. A function you can assert
// on field by field is the cheapest way to stop making it.

export interface RoutineSaveSource {
  slug: string
  name: string
  description?: string
  definition: Record<string, unknown>
  author_crew_id?: string
}

export interface RoutineSaveBody {
  slug: string
  name: string
  description: string
  definition: Record<string, unknown>
  author_crew_id?: string
  skip_test_gate: true
  skip_governance_gate?: true
}

/** Slug from a human name — lowercase, hyphenated, trimmed of edges. */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
}

/**
 * Rename the title and description, and nothing else.
 *
 * `definition.name` is what the slug derives from, so it is left alone:
 * renaming a title must not re-slug the routine out from under every
 * reference to it. The new title goes to both the `name` column (what
 * the page renders) and `display_name` (what the DSL carries), because
 * the two are read by different surfaces and disagreeing is how the
 * heading ended up showing a slug.
 *
 * skip_governance_gate is set deliberately. Save re-classifies risk and
 * lands a risky routine as `proposed`; correct for a definition edit,
 * wrong for a title. The steps are byte-identical here, so without it,
 * fixing a typo in a description demotes an approved routine to
 * "awaiting approval" — a gate firing on a change that cannot affect
 * what the routine does.
 */
export function renamePayload(
  routine: RoutineSaveSource,
  next: { name: string; description: string },
): RoutineSaveBody {
  const name = next.name.trim()
  const description = next.description.trim()
  return {
    slug: routine.slug,
    name,
    description,
    definition: { ...routine.definition, display_name: name, description },
    author_crew_id: routine.author_crew_id,
    skip_test_gate: true,
    skip_governance_gate: true,
  }
}

/**
 * Copy the definition under a new identity.
 *
 * The governance gate is NOT skipped. A duplicate is a new routine, and
 * waving it through because the original happened to be approved would
 * make Duplicate a way around review.
 */
export function duplicatePayload(
  routine: RoutineSaveSource,
  next: { name: string },
): RoutineSaveBody {
  const name = next.name.trim()
  const slug = slugify(name)
  return {
    slug,
    name,
    description: routine.description ?? "",
    definition: { ...routine.definition, name: slug, display_name: name },
    author_crew_id: routine.author_crew_id,
    skip_test_gate: true,
  }
}
