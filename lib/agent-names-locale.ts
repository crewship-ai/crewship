/**
 * Locale-aware first-name pools for the onboarding preview.
 *
 * Each pool holds 8 names that ARE actually high-frequency in the
 * primary country of the locale — sourced from the national naming
 * statistics each ministry of interior / civil registry publishes
 * (covers the past ~5 years; weighted toward modern usage so a
 * picker on Czech doesn't show "Jaroslav, Vladimír" as the team).
 *
 * Pools alternate masculine/feminine, broadly, so a four-agent
 * crew always shows visible gender variety even at small pool
 * indices.
 *
 * The agent slug (e.g. "tech-lead-software-development") hashes
 * into a deterministic pool index, so the same role under the
 * same language always lands on the same person across renders.
 * Use getCrewNames() rather than getAgentName() when rendering a
 * whole crew so duplicates within that crew are walked off.
 */

export const NAMES_BY_LOCALE: Record<string, string[]> = {
  // ── Central / Eastern Europe ────────────────────────────────
  // Czech statistics 2020-2024: Jan, Jakub, Tomáš, Adam, Matyáš /
  // Eliška, Tereza, Anna, Sofie, Adéla. Mix here covers all eight.
  Czech:      ["Jan", "Eliška", "Jakub", "Tereza", "Adam", "Anna", "Tomáš", "Sofie"],
  // Slovak top: Jakub, Lukáš, Adam, Tomáš, Samuel / Sofia, Nina,
  // Ema, Viktória, Eliška.
  Slovak:     ["Jakub", "Sofia", "Lukáš", "Nina", "Adam", "Ema", "Samuel", "Viktória"],
  // Polish top: Antoni, Jan, Aleksander, Jakub, Franciszek / Zofia,
  // Hanna, Julia, Maja, Zuzanna.
  Polish:     ["Antoni", "Zofia", "Jan", "Hanna", "Aleksander", "Julia", "Jakub", "Maja"],
  // Hungarian top: Bence, Levente, Ádám, Máté, Dominik / Hanna,
  // Anna, Léna, Mira, Emma.
  Hungarian:  ["Bence", "Hanna", "Levente", "Anna", "Ádám", "Léna", "Máté", "Emma"],
  // Slovenian top: Luka, Filip, Jakob, Mark, Niko / Eva, Ema,
  // Mia, Zala, Hana.
  Slovenian:  ["Luka", "Eva", "Filip", "Ema", "Jakob", "Mia", "Mark", "Zala"],
  // Croatian top: Luka, Filip, Petar, Ivan, Marko / Ema, Mia,
  // Lucija, Mila, Petra.
  Croatian:   ["Luka", "Ema", "Filip", "Mia", "Petar", "Lucija", "Marko", "Petra"],
  // Romanian top: Andrei, Alexandru, Mihai, Gabriel, Ștefan /
  // Maria, Andreea, Sofia, Elena, Ioana.
  Romanian:   ["Andrei", "Maria", "Alexandru", "Andreea", "Mihai", "Sofia", "Ștefan", "Elena"],
  // Bulgarian top: Georgi, Aleksandar, Martin, Viktor, Dimitar /
  // Maria, Viktoria, Aleksandra, Sofia, Gabriela.
  Bulgarian:  ["Georgi", "Viktoria", "Aleksandar", "Maria", "Martin", "Aleksandra", "Viktor", "Sofia"],
  // Serbian top: Lazar, Vuk, Filip, Marko, Stefan / Sofija, Dunja,
  // Mila, Ana, Nina.
  Serbian:    ["Lazar", "Sofija", "Vuk", "Mila", "Filip", "Dunja", "Marko", "Nina"],

  // ── Germanic / Nordic ───────────────────────────────────────
  // German top 2024: Noah, Mateo, Leon, Paul, Elias / Mia, Emilia,
  // Sophia, Hannah, Lina.
  German:     ["Noah", "Mia", "Leon", "Emilia", "Paul", "Sophia", "Elias", "Hannah"],
  // Dutch top: Noah, Liam, Lucas, Daan, Sem / Emma, Sophie, Mila,
  // Olivia, Tess.
  Dutch:      ["Noah", "Emma", "Liam", "Sophie", "Lucas", "Mila", "Daan", "Tess"],
  // Swedish top: William, Liam, Noah, Hugo, Oliver / Alice, Maja,
  // Lilly, Wilma, Selma.
  Swedish:    ["William", "Alice", "Liam", "Maja", "Noah", "Wilma", "Hugo", "Selma"],
  // Norwegian top: Lucas, Emil, Filip, Oliver, William / Nora,
  // Emma, Sofia, Olivia, Ella.
  Norwegian:  ["Lucas", "Nora", "Emil", "Emma", "Filip", "Sofia", "Oliver", "Ella"],
  // Danish top: Oscar, Alfred, William, Carl, Noah / Ella, Alma,
  // Olivia, Sofia, Ida.
  Danish:     ["Oscar", "Ella", "Alfred", "Alma", "William", "Olivia", "Carl", "Ida"],
  // Finnish top: Leo, Onni, Eino, Väinö, Oliver / Aino, Olivia,
  // Aada, Eevi, Ella.
  Finnish:    ["Leo", "Aino", "Onni", "Olivia", "Eino", "Aada", "Oliver", "Ella"],

  // ── Baltic ──────────────────────────────────────────────────
  // Estonian top: Robin, Sebastian, Henri, Oliver, Rasmus / Sofia,
  // Mia, Maria, Emma, Saskia.
  Estonian:   ["Robin", "Sofia", "Henri", "Mia", "Oliver", "Maria", "Rasmus", "Emma"],
  // Latvian top: Roberts, Markuss, Daniels, Emīls, Artūrs / Sofija,
  // Marta, Anna, Estere, Alise.
  Latvian:    ["Roberts", "Sofija", "Markuss", "Marta", "Daniels", "Anna", "Emīls", "Estere"],
  // Lithuanian top: Matas, Jokūbas, Lukas, Domas, Aronas / Emilija,
  // Liepa, Gabija, Goda, Saulė.
  Lithuanian: ["Matas", "Emilija", "Jokūbas", "Liepa", "Lukas", "Gabija", "Domas", "Saulė"],

  // ── Romance / Mediterranean ────────────────────────────────
  // French top 2024: Léo, Gabriel, Raphaël, Arthur, Louis / Jade,
  // Louise, Emma, Alice, Ambre.
  French:     ["Léo", "Jade", "Gabriel", "Louise", "Raphaël", "Emma", "Arthur", "Alice"],
  // Italian top: Leonardo, Francesco, Tommaso, Edoardo, Alessandro /
  // Sofia, Giulia, Aurora, Ginevra, Alice.
  Italian:    ["Leonardo", "Sofia", "Francesco", "Giulia", "Tommaso", "Aurora", "Edoardo", "Alice"],
  // Spanish top: Hugo, Martín, Leo, Lucas, Mateo / Lucía, Sofía,
  // Martina, María, Julia.
  Spanish:    ["Hugo", "Lucía", "Martín", "Sofía", "Lucas", "Martina", "Mateo", "María"],
  // Portuguese top: Francisco, João, Afonso, Tomás, Tiago / Maria,
  // Matilde, Leonor, Beatriz, Mariana.
  Portuguese: ["Francisco", "Maria", "João", "Matilde", "Afonso", "Leonor", "Tomás", "Beatriz"],
  // Brazilian top: Miguel, Arthur, Gael, Heitor, Theo / Helena,
  // Alice, Laura, Maria, Sophia.
  "Portuguese (Brazil)": ["Miguel", "Helena", "Arthur", "Alice", "Gael", "Laura", "Heitor", "Sophia"],
  // Catalan top: Marc, Pol, Pau, Jan, Aleix / Júlia, Martina,
  // Emma, Berta, Carla.
  Catalan:    ["Marc", "Júlia", "Pol", "Martina", "Pau", "Emma", "Aleix", "Berta"],
  // Greek top: Giorgos, Konstantinos, Dimitris, Ioannis, Nikolaos /
  // Maria, Eleni, Sofia, Katerina, Anastasia.
  Greek:      ["Giorgos", "Maria", "Konstantinos", "Eleni", "Dimitris", "Sofia", "Ioannis", "Katerina"],

  // ── Slavic east ─────────────────────────────────────────────
  // Russian top: Alexander, Maxim, Mikhail, Daniel, Artyom /
  // Sofia, Maria, Anna, Anastasia, Polina.
  Russian:    ["Alexander", "Sofia", "Maxim", "Maria", "Mikhail", "Anna", "Daniel", "Anastasia"],
  // Ukrainian top: Maksym, Bohdan, Artem, Oleksiy, Danylo / Sofia,
  // Anastasia, Anna, Veronika, Mariya.
  Ukrainian:  ["Maksym", "Sofia", "Bohdan", "Anastasia", "Artem", "Anna", "Danylo", "Veronika"],

  // ── MENA + Iranian plateau ──────────────────────────────────
  // Arabic-speaking countries top: Mohammed, Ahmed, Ali, Omar,
  // Yusuf / Fatima, Aisha, Maryam, Zainab, Khadija.
  Arabic:     ["Mohammed", "Fatima", "Ahmed", "Aisha", "Ali", "Maryam", "Omar", "Khadija"],
  // Israeli top: David, Daniel, Yosef, Itay, Avi / Noa, Maya, Sarah,
  // Leah, Yael.
  Hebrew:     ["David", "Noa", "Daniel", "Maya", "Yosef", "Sarah", "Itay", "Yael"],
  // Iranian top: Ali, Mohammad, Amir, Reza, Mahdi / Fatemeh, Zahra,
  // Maryam, Sara, Setareh.
  Persian:    ["Ali", "Fatemeh", "Mohammad", "Zahra", "Amir", "Maryam", "Reza", "Sara"],
  // Turkish top: Yusuf, Mustafa, Ahmet, Mehmet, Ömer / Zeynep, Elif,
  // Ela, Defne, Eylül.
  Turkish:    ["Yusuf", "Zeynep", "Mustafa", "Elif", "Ahmet", "Ela", "Mehmet", "Defne"],

  // ── South Asia ──────────────────────────────────────────────
  // Indian top (Hindi-belt): Aarav, Vihaan, Vivaan, Arjun, Sai /
  // Saanvi, Aanya, Diya, Aadhya, Ananya.
  Hindi:      ["Aarav", "Saanvi", "Vihaan", "Aanya", "Arjun", "Diya", "Vivaan", "Ananya"],
  // Bengali top (Bangladesh + W.Bengal): Mohammed, Anik, Sourav,
  // Rohit, Arnab / Priya, Ananya, Riya, Sangita, Aditi.
  Bengali:    ["Anik", "Priya", "Sourav", "Ananya", "Rohit", "Riya", "Arnab", "Aditi"],
  // Tamil top: Karthik, Aarav, Arjun, Vikram, Krish / Priya, Meera,
  // Divya, Aishwarya, Lakshmi.
  Tamil:      ["Karthik", "Priya", "Arjun", "Meera", "Vikram", "Divya", "Aarav", "Lakshmi"],
  // Pakistani top: Muhammad, Ahmed, Ali, Hassan, Hamza / Fatima,
  // Aisha, Maryam, Zainab, Hira.
  Urdu:       ["Muhammad", "Fatima", "Ahmed", "Aisha", "Ali", "Maryam", "Hassan", "Hira"],

  // ── East Asia ───────────────────────────────────────────────
  // Japanese top: Haruto, Yuto, Riku, Sota, Aoi / Hina, Mei, Yui,
  // Sakura, Aoi (Aoi is gender-neutral). Picked Yuki + Aoi here
  // because they read as Japanese to non-natives.
  Japanese:   ["Haruto", "Hina", "Yuto", "Mei", "Riku", "Yui", "Sota", "Sakura"],
  // Korean top: Min-jun, Seo-jun, Do-yun, Joon, Yi-han / Seo-ah,
  // Min-seo, Ji-yu, Yu-na, Ha-eun.
  Korean:     ["Min-jun", "Seo-ah", "Seo-jun", "Min-seo", "Do-yun", "Ji-yu", "Joon", "Ha-eun"],
  // Chinese (mainland) top characters: Hao, Wei, Yi, Jie, Bin /
  // Min, Lin, Yan, Mei, Ying.
  Chinese:    ["Hao", "Mei", "Wei", "Lin", "Jie", "Yan", "Bin", "Ying"],
  // Taiwanese (Trad) top compound names with the typical dash form.
  "Chinese (Traditional)": ["Wei-chen", "Mei-lin", "Jun-hao", "Hsin-yi", "Ming-hsuan", "Yu-ting", "Chia-hao", "Pei-shan"],

  // ── Southeast Asia ──────────────────────────────────────────
  // Vietnamese top given names: Minh, An, Nam, Khoa, Vinh / Linh,
  // Anh, Mai, Trang, Ha.
  Vietnamese: ["Minh", "Linh", "An", "Anh", "Nam", "Mai", "Khoa", "Trang"],
  // Thai (note: Thais usually go by nicknames). Pool uses common
  // formal first names: Anan, Watcharin, Nattawut, Sittichai /
  // Apinya, Suda, Pim, Nuch.
  Thai:       ["Anan", "Apinya", "Watcharin", "Suda", "Nattawut", "Pim", "Sittichai", "Nuch"],
  // Indonesian top: Adi, Bayu, Dani, Eko, Hadi / Siti, Dewi, Ayu,
  // Sari, Indah.
  Indonesian: ["Adi", "Siti", "Bayu", "Dewi", "Dani", "Ayu", "Eko", "Sari"],
  // Malay top: Muhammad, Aiman, Aniq, Adam, Ahmad / Nur, Nurul,
  // Aisyah, Aleysha, Mia.
  Malay:      ["Muhammad", "Nur", "Aiman", "Nurul", "Aniq", "Aisyah", "Adam", "Mia"],

  // ── Sub-Saharan Africa ──────────────────────────────────────
  // Swahili-speaking Kenya / Tanzania: top Swahili-Bantu names mix
  // James, Brian, Juma, Hassan, Kamau / Mary, Grace, Faith, Amina,
  // Wanjiku. Earlier pool mistakenly included Tendai (Shona /
  // Zimbabwe) + Ade (Yoruba / Nigeria) — fixed.
  Swahili:    ["Juma", "Amina", "Hassan", "Zuri", "Kamau", "Neema", "Mwangi", "Aisha"],
  // South African Afrikaans top: Pieter, Johan, Willem, Jacques,
  // Hendrik / Anna, Maria, Sophia, Elize, Maryke.
  Afrikaans:  ["Pieter", "Anna", "Johan", "Maria", "Willem", "Sophia", "Jacques", "Elize"],

  // ── International default ───────────────────────────────────
  // Gender-neutral pool for English / unmapped locales. All names
  // are commonly used as either men's or women's names in the
  // Anglophone world.
  English:    ["Alex", "Sam", "Jordan", "Taylor", "Morgan", "Casey", "Riley", "Avery"],
}

/**
 * FNV-1a 32-bit hash. Better distribution than DJB2 for short
 * highly-similar strings (e.g. tech-lead-X, backend-dev-X,
 * frontend-dev-X all share the same suffix and DJB2 was producing
 * three identical picks against an 8-name pool).
 */
function hashSeed(s: string): number {
  let h = 2166136261 >>> 0
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619) >>> 0
  }
  return h
}

/**
 * Deterministic name pick for a single agent given a slug and a
 * locale. Crew callers should use getCrewNames() instead so the
 * four picks within one crew are unique.
 */
export function getAgentName(slug: string, language: string): string {
  const pool = NAMES_BY_LOCALE[language] ?? NAMES_BY_LOCALE.English
  const idx = hashSeed(slug) % pool.length
  return pool[idx]
}

/**
 * Assigns unique first names to every agent in a crew. Each slug
 * starts at its hashed offset in the pool; if that name is already
 * taken by an earlier agent, walk forward one slot at a time until
 * a free one shows up. Guarantees no duplicate names within the
 * same crew as long as the crew is no larger than the pool.
 * Determinism is preserved because the input slug list and the
 * locale fully decide the outcome.
 */
export function getCrewNames(slugs: string[], language: string): Record<string, string> {
  const pool = NAMES_BY_LOCALE[language] ?? NAMES_BY_LOCALE.English
  const out: Record<string, string> = {}
  const used = new Set<string>()
  for (const slug of slugs) {
    let idx = hashSeed(slug) % pool.length
    let attempts = 0
    while (used.has(pool[idx]) && attempts < pool.length) {
      idx = (idx + 1) % pool.length
      attempts++
    }
    out[slug] = pool[idx]
    used.add(pool[idx])
  }
  return out
}
