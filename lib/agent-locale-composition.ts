/**
 * Per-slot crew composition for locales whose population is genuinely
 * multi-ethnic.
 *
 * Background: in mono-ethnic countries (Czech, Japanese, Korean,
 * Hungarian, …) drawing all four agents from the same palette mirrors
 * the reality you'd see at a real workplace and isn't tokenism in
 * either direction. In countries like the United States, France,
 * Germany, the Netherlands, Spain and Brazil, the population is
 * actively diverse and a four-of-a-kind crew would read as oblivious
 * at best, offensive at worst.
 *
 * For those locales we pre-assign each crew slot its own
 *   (avatar palette) + (name pool)
 * pair that lines up with one of the country's major demographic
 * groups. The slot ordering deliberately doesn't put the "majority"
 * face on the lead — same caution as before about reverse tokenism.
 *
 * Slug hashing into the per-slot name pool keeps determinism: the
 * same crew template under the same language always renders the same
 * team, but different crew templates show different names within
 * each group.
 *
 * Palette keys must exist in LOCALE_PALETTES (lib/agent-avatar-locale.ts).
 * We reuse the existing mono-locale palettes — they're already
 * grouped by visual look, so "Hindi" → South-Asian palette,
 * "Swahili" → Sub-Saharan, "Japanese" → East Asian, "Czech" → light
 * European, "Arabic" → MENA. The palette key is the lookup, not a
 * statement about the agent's spoken language.
 */

export interface SlotComposition {
  /** Palette key — looked up against LOCALE_PALETTES. */
  palette: string
  /** Names that read as plausible for this slot's demographic in
   *  the locale's primary country. Picked deterministically by the
   *  agent slug hash. */
  namePool: string[]
}

// ── English (US / UK / Canada / Australia composite) ─────────────
// US Census 2020 broad strokes: ~60% non-Hispanic white, ~13% Black,
// ~19% Hispanic, ~6% Asian (incl. Indian). UK: ~82% white, ~9% Asian
// (incl. Indian/Pakistani), ~4% Black. Composite mix below covers the
// four largest groups without ranking them. Names picked are common
// first names in the US/UK for each group — they READ as that group
// to a Western reader without being caricatures.

// White American / British
const EN_WHITE = ["Sarah", "Michael", "Emily", "Daniel", "Lauren", "James", "Megan", "Christopher"]
// South-Asian American / British (Indian, Pakistani, Bangladeshi)
const EN_SOUTH_ASIAN = ["Priya", "Arjun", "Ananya", "Raj", "Aanya", "Vikram", "Maya", "Karthik"]
// East-Asian American / British (Chinese, Korean, Japanese, Vietnamese)
const EN_EAST_ASIAN = ["Kevin", "Lisa", "Brian", "Jenny", "Eric", "Amy", "Andrew", "Grace"]
// Black American / British (African American + African / Caribbean diaspora)
const EN_BLACK = ["Marcus", "Jasmine", "Devon", "Aaliyah", "Malik", "Imani", "Andre", "Zora"]
// Hispanic / Latino American (significant US demographic, less so in UK)
const EN_HISPANIC = ["Sofia", "Diego", "Carmen", "Mateo", "Camila", "Carlos", "Valentina", "Javier"]

// ── French (Maghreb + Sub-Saharan immigrant communities) ─────────
// Metropolitan France: ~85% European French, ~7% Maghrebi (Algerian /
// Moroccan / Tunisian), ~3% Sub-Saharan African. Slot mix uses one
// of each below the two European-French slots.
const FR_EUROPEAN = ["Léo", "Emma", "Gabriel", "Louise", "Raphaël", "Alice", "Hugo", "Jade"]
const FR_MAGHREB = ["Karim", "Yasmine", "Mehdi", "Inès", "Sami", "Sarah", "Adam", "Lina"]
const FR_AFRICAN = ["Mamadou", "Aïcha", "Ousmane", "Aminata", "Issa", "Fatou", "Moussa", "Awa"]

// ── German (Turkish + MENA + Eastern-European immigrant groups) ──
const DE_NATIVE = ["Noah", "Mia", "Leon", "Sophia", "Paul", "Emma", "Elias", "Hannah"]
const DE_TURKISH = ["Emre", "Aylin", "Yusuf", "Defne", "Ali", "Zeynep", "Mehmet", "Selin"]
const DE_MENA = ["Karim", "Layla", "Omar", "Yasmin", "Ahmed", "Mariam", "Hassan", "Nour"]

// ── Dutch (Indonesian + Moroccan + Surinamese diaspora) ──────────
const NL_NATIVE = ["Noah", "Emma", "Liam", "Sophie", "Daan", "Mila", "Sem", "Tess"]
const NL_INDO = ["Aiden", "Ayu", "Wahyu", "Anouk", "Adi", "Indah", "Bayu", "Sari"]
const NL_MOROCCAN = ["Mohammed", "Aisha", "Yassin", "Fatima", "Karim", "Layla", "Adam", "Yasmin"]

// ── Spanish (Spain — Maghrebi minority + Latin-American immigrants) ─
const ES_NATIVE = ["Hugo", "Lucía", "Martín", "Sofía", "Lucas", "Martina", "Mateo", "María"]
const ES_MAGHREB = ["Karim", "Yasmin", "Mohamed", "Sara", "Adam", "Aisha", "Hamza", "Layla"]
const ES_LATAM = ["Diego", "Camila", "Mateo", "Valentina", "Santiago", "Isabella", "Sebastián", "Sofía"]

// ── Portuguese (Brazil — extreme internal mix) ─────────────────
// Brazilian Census 2022: ~43% pardo (mixed-race), ~48% white,
// ~10% Black, ~1% indigenous + Asian. We oversample non-white to
// reflect that white-Brazilian is barely a plurality in modern
// demographics. Names are all Brazilian Portuguese first names —
// Brazilians of every background carry these.
const BR_LIGHT = ["Miguel", "Helena", "Arthur", "Alice"]
const BR_AFRO = ["Beatriz", "João Pedro", "Aisha", "Caio"]
const BR_INDIGENOUS_MIX = ["Davi", "Manuela", "Théo", "Lara"]
const BR_ASIAN = ["Yuki", "Henrique", "Sakura", "Daniel"]

/**
 * Map of locale → 4-slot crew composition. Locales not listed here
 * use the simple single-pool path (all agents from one palette / one
 * name pool) — that's correct for mono-ethnic countries.
 *
 * Slot order is deliberately MIXED so the lead position isn't always
 * the majority group — same care as before about not making the
 * "diverse" agent always a junior role.
 */
export const DIVERSE_CREW_COMPOSITIONS: Record<string, SlotComposition[]> = {
  // English (US/UK reading): South Asian → Black → White → East Asian
  // The lead being South Asian reflects that group's overrepresentation
  // in tech leadership; the four-slot rotation includes the largest
  // groups so no major demographic is "missing" from the team picture.
  English: [
    { palette: "Hindi",    namePool: EN_SOUTH_ASIAN },
    { palette: "Swahili",  namePool: EN_BLACK },
    { palette: "Czech",    namePool: EN_WHITE },
    { palette: "Japanese", namePool: EN_EAST_ASIAN },
  ],
  French: [
    { palette: "French",   namePool: FR_EUROPEAN },
    { palette: "Arabic",   namePool: FR_MAGHREB },
    { palette: "French",   namePool: FR_EUROPEAN },
    { palette: "Swahili",  namePool: FR_AFRICAN },
  ],
  German: [
    { palette: "German",   namePool: DE_NATIVE },
    { palette: "Turkish",  namePool: DE_TURKISH },
    { palette: "German",   namePool: DE_NATIVE },
    { palette: "Arabic",   namePool: DE_MENA },
  ],
  Dutch: [
    { palette: "Dutch",       namePool: NL_NATIVE },
    { palette: "Indonesian",  namePool: NL_INDO },
    { palette: "Dutch",       namePool: NL_NATIVE },
    { palette: "Arabic",      namePool: NL_MOROCCAN },
  ],
  Spanish: [
    { palette: "Spanish",   namePool: ES_NATIVE },
    { palette: "Arabic",    namePool: ES_MAGHREB },
    { palette: "Spanish",   namePool: ES_LATAM },
    { palette: "Spanish",   namePool: ES_NATIVE },
  ],
  // Brazilian crew mix: pardo (mixed-race) + white + Black + Asian
  // matches the 2022 census broad strokes. Italian palette key
  // gives a Mediterranean tone for the pardo slot; Japanese for the
  // Asian-Brazilian slot (São Paulo hosts the largest Japanese
  // diaspora outside Japan). All four slots use Brazilian Portuguese
  // first names — those carry across every background in Brazil.
  "Portuguese (Brazil)": [
    { palette: "Swahili",              namePool: BR_AFRO },
    { palette: "Portuguese (Brazil)",  namePool: BR_LIGHT },
    { palette: "Italian",              namePool: BR_INDIGENOUS_MIX },
    { palette: "Japanese",             namePool: BR_ASIAN },
  ],
}

/**
 * Reserved for a future 5-slot crew expansion — Hispanic Americans
 * are the second-largest US group (~19% of the population) but
 * only the fifth-largest in tech specifically, so they sit below
 * White / East Asian / South Asian / Black for the current 4-slot
 * English mix. Export so a future contributor can wire it up
 * without re-researching the name set.
 */
export const EN_HISPANIC_POOL = EN_HISPANIC
