/**
 * Per-slot crew composition for locales whose population is genuinely
 * multi-ethnic.
 *
 * Background — same as the previous iteration: mono-ethnic countries
 * (Czech, Japanese, Korean, Hungarian, …) get all four agents from
 * one palette / one name pool, which matches reality and isn't
 * tokenism. Multi-ethnic countries (United States, United Kingdom,
 * France, Germany, the Netherlands, Spain, Brazil, …) get a slot
 * mix that reflects each country's actual demographic spread.
 *
 * Two upgrades from the v1 pass:
 *
 * 1. Pools are now ~15–20 names per demographic group (was 4–8) so
 *    a user who switches between crew templates and languages sees
 *    fresh teammates each time rather than the same Sarah/Marcus
 *    appearing in every preview.
 *
 * 2. Selection is RANDOMISED at the call site, not deterministic by
 *    slug hash. computeRandomCrew() rolls a fresh draw on every
 *    invocation; the OnboardingPreview holds the result in React
 *    state and re-rolls only when (language | crew template)
 *    changes. Within a single render the picks are stable.
 *
 * Palette keys must exist in LOCALE_PALETTES (lib/agent-avatar-locale.ts).
 * We reuse the existing mono-locale palettes — they're already
 * grouped by visual look ("Hindi" → South-Asian palette,
 * "Swahili" → Sub-Saharan, "Japanese" → East Asian, "Czech" → light
 * European, "Arabic" → MENA). The palette key is the lookup, not a
 * statement about the agent's spoken language.
 */

import { NAMES_BY_LOCALE } from "./agent-names-locale"

export interface SlotComposition {
  /** Palette key — looked up against LOCALE_PALETTES. */
  palette: string
  /** First name shown for this slot — picked from the slot's pool. */
  name: string
}

interface SlotTemplate {
  palette: string
  namePool: string[]
}

// ──────────────────────────────────────────────────────────────────
// United States demographic name pools
// ──────────────────────────────────────────────────────────────────
// White US (Social Security top names + classic US first names)
const US_WHITE = [
  "Sarah", "Michael", "Emily", "Daniel", "Lauren", "James", "Megan", "Christopher",
  "Jessica", "Andrew", "Hannah", "Ryan", "Ashley", "Joshua", "Madison", "Tyler",
  "Olivia", "Brandon", "Amanda", "Jacob",
]
// Black / African-American (frequent first names per US census + cultural pools)
const US_BLACK = [
  "Marcus", "Jasmine", "Devon", "Aaliyah", "Malik", "Imani", "Andre", "Zora",
  "Trevon", "Tasha", "DeAndre", "Janae", "Jamal", "Ebony", "Marquis", "Aniyah",
  "Darnell", "Nia", "Tyrone", "Latoya",
]
// Hispanic / Latino US (US Census top Hispanic first names)
const US_HISPANIC = [
  "Sofia", "Diego", "Carmen", "Mateo", "Camila", "Carlos", "Valentina", "Javier",
  "Isabella", "Santiago", "Lucia", "Sebastián", "Ximena", "Manuel", "Ana", "Luis",
  "Mariana", "Alejandro", "Elena", "José",
]
// East-Asian American (Chinese-, Korean-, Japanese-, Vietnamese-American
// first names — frequent Anglicised picks).
const US_EAST_ASIAN = [
  "Kevin", "Lisa", "Brian", "Jenny", "Eric", "Amy", "Andrew", "Grace",
  "Justin", "Tiffany", "David", "Christina", "Wesley", "Michelle", "Eugene", "Connie",
  "Tony", "Stephanie", "Calvin", "Vivian",
]
// South-Asian American (Indian-, Pakistani-, Bangladeshi-American).
const US_SOUTH_ASIAN = [
  "Priya", "Arjun", "Ananya", "Raj", "Aanya", "Vikram", "Maya", "Karthik",
  "Anika", "Rohan", "Diya", "Sanjay", "Neha", "Anish", "Pooja", "Nikhil",
  "Aisha", "Amit", "Riya", "Suresh",
]

// ──────────────────────────────────────────────────────────────────
// United Kingdom demographic name pools
// ──────────────────────────────────────────────────────────────────
// White British (ONS top names, plus established British classics)
const UK_WHITE = [
  "Oliver", "Olivia", "Harry", "Amelia", "Jack", "Isla", "George", "Sophie",
  "Charlie", "Lily", "Thomas", "Ella", "Henry", "Mia", "Theo", "Grace",
  "William", "Freya", "Arthur", "Emily",
]
// Black British (African + Caribbean diaspora common first names)
const UK_BLACK = [
  "Amara", "Kwame", "Anaya", "Tobi", "Zuri", "Olu", "Imani", "Kofi",
  "Naomi", "Femi", "Aaliyah", "Daniel", "Ayana", "Marcus", "Akua", "Ade",
  "Sade", "Jermaine", "Nia", "Kelvin",
]
// British Asian — South Asian (Indian / Pakistani / Bangladeshi)
const UK_SOUTH_ASIAN = [
  "Aisha", "Mohammed", "Priya", "Arjun", "Fatima", "Raj", "Zara", "Imran",
  "Anaya", "Hassan", "Layla", "Ravi", "Aaliyah", "Yusuf", "Maya", "Kabir",
  "Saira", "Aarav", "Nisha", "Faisal",
]
// British East Asian (Chinese / Hong-Kong / Vietnamese diaspora)
const UK_EAST_ASIAN = [
  "Wei", "Mei", "Jun", "Lin", "Hao", "Xia", "Yan", "Jing",
  "Daniel", "Grace", "Wing", "Ming", "Chen", "Lucy", "Bao", "Hong",
  "Yuki", "Anna", "Tony", "Sophie",
]

// ──────────────────────────────────────────────────────────────────
// France (Metropolitan French + Maghrebi + Sub-Saharan African
// communities — ~13% of the population combined)
// ──────────────────────────────────────────────────────────────────
const FR_EUROPEAN = [
  "Léo", "Emma", "Gabriel", "Louise", "Raphaël", "Alice", "Hugo", "Jade",
  "Arthur", "Ambre", "Louis", "Inès", "Adam", "Anna", "Jules", "Léna",
  "Lucas", "Mia", "Noah", "Chloé",
]
const FR_MAGHREB = [
  "Karim", "Yasmine", "Mehdi", "Inès", "Sami", "Sarah", "Adam", "Lina",
  "Rayan", "Lila", "Bilal", "Nour", "Hamza", "Maya", "Ilyes", "Imane",
  "Naël", "Aya", "Adel", "Salma",
]
const FR_AFRICAN = [
  "Mamadou", "Aïcha", "Ousmane", "Aminata", "Issa", "Fatou", "Moussa", "Awa",
  "Amadou", "Mariama", "Sékou", "Adama", "Ibrahima", "Mariam", "Cheikh", "Astou",
  "Souleymane", "Khadija", "Boubacar", "Bintou",
]

// ──────────────────────────────────────────────────────────────────
// Germany (German + Turkish-German + MENA-German communities)
// ──────────────────────────────────────────────────────────────────
const DE_NATIVE = [
  "Noah", "Mia", "Leon", "Sophia", "Paul", "Emma", "Elias", "Hannah",
  "Ben", "Anna", "Felix", "Lina", "Lukas", "Marie", "Jonas", "Lena",
  "Finn", "Clara", "Maximilian", "Greta",
]
const DE_TURKISH = [
  "Emre", "Aylin", "Yusuf", "Defne", "Ali", "Zeynep", "Mehmet", "Selin",
  "Hakan", "Elif", "Berkan", "Esra", "Onur", "Ayşe", "Kerem", "Beyza",
  "Burak", "Merve", "Eren", "Damla",
]
const DE_MENA = [
  "Karim", "Layla", "Omar", "Yasmin", "Ahmed", "Mariam", "Hassan", "Nour",
  "Adam", "Sarah", "Bilal", "Lina", "Mohammed", "Aya", "Tariq", "Salma",
  "Rashid", "Nadia", "Faisal", "Inas",
]

// ──────────────────────────────────────────────────────────────────
// Netherlands (Native Dutch + Indonesian-Dutch + Moroccan-Dutch +
// Surinamese-Dutch communities)
// ──────────────────────────────────────────────────────────────────
const NL_NATIVE = [
  "Noah", "Emma", "Liam", "Sophie", "Daan", "Mila", "Sem", "Tess",
  "Levi", "Olivia", "Lucas", "Lotte", "Finn", "Julia", "Bram", "Anna",
  "Sven", "Eva", "Thijs", "Saar",
]
const NL_INDO = [
  "Aiden", "Ayu", "Wahyu", "Anouk", "Adi", "Indah", "Bayu", "Sari",
  "Hadi", "Dewi", "Joko", "Ratih", "Bagus", "Mira", "Eko", "Putri",
  "Surya", "Wati", "Iwan", "Lily",
]
const NL_MOROCCAN = [
  "Mohammed", "Aisha", "Yassin", "Fatima", "Karim", "Layla", "Adam", "Yasmin",
  "Bilal", "Salma", "Hamza", "Mariam", "Anis", "Nour", "Ilyas", "Lina",
  "Tariq", "Sara", "Younes", "Iman",
]

// ──────────────────────────────────────────────────────────────────
// Spain (European Spanish + Latin-American + Maghrebi communities)
// ──────────────────────────────────────────────────────────────────
const ES_NATIVE = [
  "Hugo", "Lucía", "Martín", "Sofía", "Lucas", "Martina", "Mateo", "María",
  "Daniel", "Valeria", "Pablo", "Paula", "Álvaro", "Carmen", "Adrián", "Julia",
  "Diego", "Alba", "Mario", "Noa",
]
const ES_MAGHREB = [
  "Karim", "Yasmin", "Mohamed", "Sara", "Adam", "Aisha", "Hamza", "Layla",
  "Anis", "Imane", "Bilal", "Salma", "Younes", "Nour", "Mehdi", "Lina",
  "Tariq", "Mariam", "Rachid", "Aya",
]
const ES_LATAM = [
  "Diego", "Camila", "Mateo", "Valentina", "Santiago", "Isabella", "Sebastián", "Sofía",
  "Joaquín", "Renata", "Tomás", "Mariana", "Benjamín", "Ximena", "Maximiliano", "Antonella",
  "Vicente", "Catalina", "Emiliano", "Florencia",
]

// ──────────────────────────────────────────────────────────────────
// Brazil — pardo (mixed) + light + Afro-Brazilian + Asian-Brazilian
// (São Paulo hosts the largest Japanese diaspora outside Japan).
// All four pools use Brazilian Portuguese first names — they carry
// across every background in Brazil.
// ──────────────────────────────────────────────────────────────────
const BR_LIGHT = [
  "Miguel", "Helena", "Arthur", "Alice", "Heitor", "Laura", "Theo", "Maria",
  "Davi", "Sophia", "Bernardo", "Manuela", "Gabriel", "Cecília", "Lorenzo", "Eloá",
  "Pedro", "Sarah", "Enzo", "Lara",
]
const BR_PARDO = [
  "Davi", "Manuela", "Théo", "Lara", "Caio", "Júlia", "Ravi", "Mariana",
  "Lucas", "Beatriz", "Yuri", "Isabela", "Felipe", "Olivia", "Diego", "Vitória",
  "Henrique", "Clara", "André", "Catarina",
]
const BR_AFRO = [
  "Beatriz", "João Pedro", "Aisha", "Caio", "Iara", "Murilo", "Zora", "Bento",
  "Aline", "Augusto", "Tainá", "Heitor", "Yara", "Antônio", "Naara", "Davi",
  "Solange", "Marcos", "Janaína", "Pablo",
]
const BR_ASIAN = [
  "Yuki", "Henrique", "Sakura", "Daniel", "Aki", "Caio", "Yumi", "Rafael",
  "Hana", "Lucas", "Mei", "Pedro", "Aoi", "André", "Sora", "Bruno",
  "Rin", "Tomás", "Mio", "Vitor",
]

/**
 * Composition templates per multi-ethnic locale. The slot ORDER
 * determines which crew role gets which demographic — chosen so
 * the lead position is never always the same group across all
 * locales (avoids both "majority-as-lead" and "minority-as-lead-
 * for-show" pitfalls). Each crew render picks ONE name at random
 * from each slot's pool.
 */
export const DIVERSE_CREW_COMPOSITIONS: Record<string, SlotTemplate[]> = {
  // US: ~60% white, ~19% Hispanic, ~13% Black, ~6% Asian (East + S).
  // 4-slot crew with one from each major group keeps every preview
  // visibly diverse rather than 3-white-1-other on average. Slot
  // ordering rotates so the lead isn't always the same demographic.
  "English (US)": [
    { palette: "Hindi",    namePool: US_SOUTH_ASIAN },
    { palette: "Spanish",  namePool: US_HISPANIC },
    { palette: "Swahili",  namePool: US_BLACK },
    { palette: "Czech",    namePool: US_WHITE },
  ],
  // Canadian English — close to US mix but with more S-Asian + East-
  // Asian (Toronto / Vancouver) and less Black. Reuses US pools for
  // budget; can be customised if needed.
  "English (Canada)": [
    { palette: "Hindi",    namePool: US_SOUTH_ASIAN },
    { palette: "Japanese", namePool: US_EAST_ASIAN },
    { palette: "Czech",    namePool: US_WHITE },
    { palette: "Swahili",  namePool: US_BLACK },
  ],
  // Australian English — ~76% white, ~17% Asian (East + S), small
  // Indigenous + African / Pacific Islander minorities. Reusing US
  // pools as a best-fit approximation pending Australia-specific
  // research.
  "English (Australia)": [
    { palette: "Czech",    namePool: UK_WHITE },
    { palette: "Japanese", namePool: UK_EAST_ASIAN },
    { palette: "Hindi",    namePool: UK_SOUTH_ASIAN },
    { palette: "Czech",    namePool: UK_WHITE },
  ],
  // UK (default "English" — flag is GB). ONS-aligned mix: ~82% white
  // British, ~9% Asian (heavy S. Asian), ~4% Black.
  English: [
    { palette: "Czech",    namePool: UK_WHITE },
    { palette: "Hindi",    namePool: UK_SOUTH_ASIAN },
    { palette: "Swahili",  namePool: UK_BLACK },
    { palette: "Japanese", namePool: UK_EAST_ASIAN },
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
  "Portuguese (Brazil)": [
    { palette: "Swahili",              namePool: BR_AFRO },
    { palette: "Portuguese (Brazil)",  namePool: BR_LIGHT },
    { palette: "Italian",              namePool: BR_PARDO },
    { palette: "Japanese",             namePool: BR_ASIAN },
  ],
}

/**
 * Pick a random element from an array. Tiny wrapper so the call site
 * reads cleanly and so a future deterministic seed (PRNG, test mock)
 * has one swap point.
 */
function pickRandom<T>(pool: T[]): T {
  return pool[Math.floor(Math.random() * pool.length)]
}

/**
 * Returns a fresh crew composition. Multi-ethnic locales draw one
 * name per slot from the slot's demographic pool; mono-ethnic
 * locales draw `slotCount` distinct names from the single locale
 * pool (Fisher-Yates partial shuffle for uniqueness).
 *
 * Random per call — components should hold the result in React
 * state and re-roll only when (language | crew template) changes.
 */
export function computeRandomCrew(language: string, slotCount: number): SlotComposition[] {
  const diverse = DIVERSE_CREW_COMPOSITIONS[language]
  if (diverse) {
    return Array.from({ length: slotCount }, (_, i) => {
      const slot = diverse[i % diverse.length]
      return { palette: slot.palette, name: pickRandom(slot.namePool) }
    })
  }
  // Mono-locale: pick N unique names from the locale's single pool.
  // Partial Fisher-Yates so we don't allocate the full shuffled list
  // when the crew is small.
  const pool = NAMES_BY_LOCALE[language] ?? NAMES_BY_LOCALE.English
  const copy = [...pool]
  const out: SlotComposition[] = []
  const draws = Math.min(slotCount, copy.length)
  for (let i = 0; i < draws; i++) {
    const j = i + Math.floor(Math.random() * (copy.length - i))
    ;[copy[i], copy[j]] = [copy[j], copy[i]]
    out.push({ palette: language, name: copy[i] })
  }
  // If we ran out of pool, fall back to repeating (shouldn't happen
  // with 20-name pools and 4-agent crews).
  while (out.length < slotCount) {
    out.push({ palette: language, name: pickRandom(pool) })
  }
  return out
}
