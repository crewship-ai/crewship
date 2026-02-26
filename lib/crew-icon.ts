import { createAvatar } from "@dicebear/core"
import * as icons from "@dicebear/icons"

const iconsStyle = icons as Parameters<typeof createAvatar>[0]

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const iconSchema = (icons as any).schema?.properties?.icon?.items?.enum as string[] | undefined
export const ALL_CREW_ICONS: string[] = iconSchema ?? []

export const ICON_COLORS = [
  "90caf9", "ffe082", "80deea", "ffab91", "b39ddb",
  "a5d6a7", "f48fb1", "ce93d8", "ef9a9a", "80cbc4",
  "ffcc80", "c5e1a5",
]

const CATEGORY_MAP: Record<string, string[]> = {
  business: ["briefcase", "building", "bank", "cashCoin", "piggyBank", "wallet", "wallet2", "calculator", "shopWindow", "shop", "cart2"],
  engineering: ["bug", "hdd", "keyboard", "laptop", "display", "plug", "router", "sdCard", "webcam", "printer", "mouse"],
  development: ["bug", "keyboard", "laptop", "display", "lightbulb", "puzzle", "pencil", "pen", "book", "lightningCharge"],
  design: ["brush", "palette", "palette2", "paintBucket", "easel", "gem", "scissors", "pen", "pencil", "camera"],
  operations: ["boxes", "boxSeam", "box", "truck", "truckFlatbed", "minecart", "minecartLoaded", "clock", "stopwatch", "stoplights"],
  marketing: ["megaphone", "star", "trophy", "award", "flag", "globe", "globe2", "newspaper", "send", "envelope"],
  security: ["lock", "key", "eyeglasses", "binoculars", "search", "bug"],
  quality: ["award", "trophy", "star", "search", "pencil", "gem"],
  communication: ["envelope", "mailbox", "phone", "megaphone", "send", "bell", "newspaper"],
  logistics: ["truck", "truckFlatbed", "boxes", "boxSeam", "box", "minecart", "minecartLoaded", "basket", "signpost2", "signpostSplit", "compass", "map"],
  science: ["thermometer", "lightbulb", "brightnessHigh", "droplet", "lightning", "tornado", "tsunami", "snow", "sun", "moonStars"],
  education: ["book", "bookshelf", "mortarboard", "pencil", "pen", "lightbulb", "calculator", "puzzle", "lamp"],
  finance: ["cashCoin", "piggyBank", "bank", "wallet", "wallet2", "calculator", "coin", "briefcase"],
  support: ["phone", "envelope", "send", "bell", "bandaid", "heart", "handThumbsUp", "umbrella"],
  home: ["house", "houseDoor", "doorOpen", "doorClosed", "lamp", "lightbulb", "key", "lock"],
}

export const CREW_ICON_CATEGORIES = Object.keys(CATEGORY_MAP)

export function searchCrewIcons(query: string): string[] {
  if (!query.trim()) return ALL_CREW_ICONS.slice(0, 24)

  const q = query.toLowerCase()

  const categoryMatch = CATEGORY_MAP[q]
  if (categoryMatch) {
    return categoryMatch.filter((i) => ALL_CREW_ICONS.includes(i))
  }

  const fuzzy = ALL_CREW_ICONS.filter((name) => name.toLowerCase().includes(q))
  if (fuzzy.length > 0) return fuzzy

  for (const [cat, catIcons] of Object.entries(CATEGORY_MAP)) {
    if (cat.includes(q)) {
      return catIcons.filter((i) => ALL_CREW_ICONS.includes(i))
    }
  }

  return ALL_CREW_ICONS.slice(0, 24)
}

export function getCrewIconUrl(iconNameOrSeed: string, bgColor?: string | null): string {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const opts: any = { seed: iconNameOrSeed, size: 128 }
  if (ALL_CREW_ICONS.includes(iconNameOrSeed)) {
    opts.icon = [iconNameOrSeed]
  }
  if (bgColor) {
    opts.backgroundColor = [bgColor.replace("#", "")]
  }
  return createAvatar(iconsStyle, opts).toDataUri()
}
