/**
 * Locale-aware first-name pools for the onboarding preview.
 *
 * Each pool mixes masculine and feminine names common in that
 * language's primary region. The agent slug (e.g.
 * "tech-lead-software-development") seeds a deterministic index
 * into the pool so the same role under the same language always
 * lands on the same person — predictable preview, no flicker on
 * re-render, no churn when the user switches the picker back and
 * forth.
 *
 * Pools are 8 names each, alternating broadly between masculine
 * and feminine so a four-agent crew shows visible gender variety
 * even at small pool indices.
 *
 * Coverage tracks the language catalog at lib/languages.ts. A
 * locale we don't list falls back to the English mixed pool.
 */

export const NAMES_BY_LOCALE: Record<string, string[]> = {
  // Central / Eastern Europe
  Czech:      ["Tomáš", "Anna", "Jakub", "Tereza", "Petr", "Eliška", "Lukáš", "Klára"],
  Slovak:     ["Marek", "Eva", "Peter", "Sofia", "Jakub", "Nina", "Lukáš", "Lenka"],
  Polish:     ["Piotr", "Anna", "Tomasz", "Maria", "Jakub", "Zofia", "Adam", "Julia"],
  Hungarian:  ["Bence", "Anna", "Ádám", "Sofia", "Levente", "Lili", "Márton", "Hanna"],
  Slovenian:  ["Luka", "Nina", "Jure", "Eva", "Matej", "Sara", "Tilen", "Maja"],
  Croatian:   ["Marko", "Ana", "Luka", "Ivana", "Filip", "Petra", "Ivan", "Mia"],
  Romanian:   ["Andrei", "Maria", "Mihai", "Ioana", "Ștefan", "Andreea", "Alexandru", "Elena"],
  Bulgarian:  ["Georgi", "Maria", "Ivan", "Elena", "Nikolay", "Petya", "Stoyan", "Tsvetelina"],
  Serbian:    ["Marko", "Ana", "Stefan", "Milica", "Nikola", "Jelena", "Aleksandar", "Sofia"],

  // Germanic / Nordic
  German:     ["Hans", "Anna", "Lukas", "Sophie", "Stefan", "Lena", "Felix", "Julia"],
  Dutch:      ["Jan", "Anna", "Pieter", "Eva", "Bas", "Sophie", "Hendrik", "Emma"],
  Swedish:    ["Erik", "Anna", "Lars", "Astrid", "Magnus", "Linnea", "Anders", "Sofia"],
  Norwegian:  ["Erik", "Ingrid", "Lars", "Anna", "Magnus", "Sofie", "Henrik", "Maria"],
  Danish:     ["Lars", "Anna", "Mikkel", "Sofie", "Jens", "Maria", "Henrik", "Ida"],
  Finnish:    ["Mikko", "Aino", "Antti", "Liisa", "Juha", "Sofia", "Janne", "Emma"],

  // Baltic
  Estonian:   ["Mart", "Liis", "Andres", "Maria", "Toomas", "Anna", "Kristjan", "Sofia"],
  Latvian:    ["Jānis", "Anna", "Mārtiņš", "Liene", "Pēteris", "Kristīne", "Andris", "Elīna"],
  Lithuanian: ["Mantas", "Aušra", "Tomas", "Eglė", "Lukas", "Rasa", "Andrius", "Gabija"],

  // Romance / Mediterranean
  French:     ["Pierre", "Marie", "Antoine", "Camille", "Lucas", "Sophie", "Hugo", "Léa"],
  Italian:    ["Marco", "Sofia", "Luca", "Giulia", "Andrea", "Aurora", "Matteo", "Alice"],
  Spanish:    ["Carlos", "María", "Pablo", "Lucía", "Diego", "Sofía", "Sergio", "Carmen"],
  Portuguese: ["João", "Maria", "Miguel", "Beatriz", "Tiago", "Sofia", "Rafael", "Carolina"],
  "Portuguese (Brazil)": ["João", "Maria", "Pedro", "Beatriz", "Lucas", "Sofia", "Gabriel", "Helena"],
  Catalan:    ["Marc", "Laia", "Pau", "Júlia", "Aleix", "Martina", "Pol", "Berta"],
  Greek:      ["Giorgos", "Maria", "Dimitris", "Eleni", "Nikos", "Sofia", "Yannis", "Katerina"],

  // Slavic east
  Russian:    ["Dmitry", "Anna", "Alexei", "Olga", "Mikhail", "Tatiana", "Pavel", "Elena"],
  Ukrainian:  ["Oleksandr", "Olena", "Andriy", "Anna", "Mykhailo", "Sofia", "Dmytro", "Kateryna"],

  // MENA + Iranian plateau
  Arabic:     ["Ahmed", "Fatima", "Mohammed", "Aisha", "Omar", "Maryam", "Khalid", "Zainab"],
  Hebrew:     ["David", "Sarah", "Yosef", "Rachel", "Avi", "Leah", "Daniel", "Noa"],
  Persian:    ["Ali", "Fatemeh", "Reza", "Maryam", "Hamid", "Zahra", "Hossein", "Sara"],
  Turkish:    ["Mehmet", "Ayşe", "Ahmet", "Fatma", "Mustafa", "Zeynep", "Ali", "Elif"],

  // South Asia
  Hindi:      ["Raj", "Priya", "Amit", "Anjali", "Arjun", "Riya", "Vikram", "Aarti"],
  Bengali:    ["Anik", "Priya", "Rohit", "Sangita", "Sourav", "Anjali", "Arjun", "Riya"],
  Tamil:      ["Karthik", "Priya", "Arjun", "Anjali", "Vikram", "Meera", "Suresh", "Devi"],
  Urdu:       ["Ali", "Fatima", "Ahmed", "Aisha", "Hamza", "Zainab", "Hassan", "Maryam"],

  // East Asia
  Japanese:   ["Hiroshi", "Yuki", "Akira", "Sakura", "Takashi", "Aiko", "Kenji", "Hana"],
  Korean:     ["Min-ho", "So-yeon", "Ji-hoon", "Min-ji", "Tae-hyun", "Ji-woo", "Joon", "Yeon-ah"],
  Chinese:    ["Wei", "Mei", "Jun", "Lin", "Ming", "Xia", "Hao", "Yan"],
  "Chinese (Traditional)": ["Wei-chen", "Mei-lin", "Jun-hao", "Hsin-yi", "Ming-hsuan", "Yu-ting", "Chia-hao", "Pei-shan"],

  // Southeast Asia
  Vietnamese: ["Minh", "Linh", "Nam", "Lan", "Hùng", "Mai", "Bảo", "Anh"],
  Thai:       ["Somchai", "Pim", "Anan", "Suda", "Kasem", "Nuch", "Niran", "Apinya"],
  Indonesian: ["Budi", "Siti", "Adi", "Dewi", "Joko", "Sari", "Bambang", "Ayu"],
  Malay:      ["Ahmad", "Siti", "Hassan", "Aisyah", "Faizal", "Nurul", "Rahman", "Aminah"],

  // Sub-Saharan Africa
  Swahili:    ["Kamau", "Amina", "Juma", "Zara", "Tendai", "Aisha", "Ade", "Neema"],
  Afrikaans:  ["Pieter", "Anna", "Johan", "Maria", "Willem", "Sophia", "Jan", "Elsie"],

  // English — international/mixed pool, biased toward names that
  // read clearly in any region.
  English:    ["Alex", "Sam", "Jordan", "Taylor", "Morgan", "Casey", "Riley", "Avery"],
}

/**
 * Cheap string hash → non-negative integer. Used to pick a name
 * from the pool deterministically from a slug. Mirrors the
 * standard DJB2 variant so any agent slug produces the same
 * index across renders and across users.
 */
function hashSeed(s: string): number {
  let h = 5381
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) + h + s.charCodeAt(i)) >>> 0
  }
  return h
}

/**
 * Deterministic name pick for an agent given a slug and a locale.
 * Falls through to the English mixed pool when we don't have a map
 * for the requested language, so adding a new entry to
 * lib/languages.ts never makes the preview crash — at worst the
 * user sees neutral English names until we extend this file.
 */
export function getAgentName(slug: string, language: string): string {
  const pool = NAMES_BY_LOCALE[language] ?? NAMES_BY_LOCALE.English
  const idx = hashSeed(slug) % pool.length
  return pool[idx]
}
