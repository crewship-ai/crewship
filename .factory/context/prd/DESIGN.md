# Crewship -- Design System

**Verze:** 2.0
**Datum:** 2026-02-11
**Inspirace:** Meta Business Suite (cistota, karty, whitespace, barvy)

---

## Stack

| Knihovna | Verze | Poznamka |
|---|---|---|
| Tailwind CSS | 4.1.18 | CSS-first config (`@theme inline`), **ne** tailwind.config.ts |
| shadcn/ui | latest (TW4) | `npx shadcn@latest init`, OKLCH barvy, `data-slot` atributy |
| tw-animate-css | latest | Nahradi deprecated `tailwindcss-animate` |
| lucide-react | 0.563.0 | Ikonova knihovna (jedina povolena) |
| Inter | variable font | `next/font/google` -- hlavni font |
| JetBrains Mono | variable font | `next/font/google` -- monospace pro logy a kod |
| next-themes | latest | Dark mode provider |
| React | 19.2 | Bez forwardRef, s `ref` jako prop |
| Next.js | 16.1 | App Router, RSC, Turbopack |

> **POZOR:** Tailwind v4 nepouziva `tailwind.config.ts`. Vse se konfiguruje
> primo v `app/globals.css` pres `@theme inline` direktivu.
> Nas soucasny `tailwind.config.ts` se pri migraci smaze.

---

## 1. Barevna paleta

Barvy co nejvic podobne Meta Business Suite. Vlastni brand akcenty
pouzivame jen tam kde to dava smysl (premium badge, status running).

### 1.1 Primary (Meta blue rodina)

| Token | HEX | OKLCH | Pouziti |
|---|---|---|---|
| primary-950 | `#003366` | `0.27 0.10 245` | Nejtmavsi text na primary bg |
| primary-900 | `#0D4F8B` | `0.37 0.12 245` | Dark mode primary |
| primary-800 | `#1565A8` | `0.44 0.13 240` | Hover v dark mode |
| primary-700 | `#1877C2` | `0.50 0.14 240` | Aktivni stav |
| **primary-600** | **`#1877F2`** | **`0.55 0.17 250`** | **Meta blue -- hlavni CTA, linky** |
| primary-500 | `#4293F5` | `0.63 0.15 250` | Hover na CTA |
| primary-400 | `#6BAAF7` | `0.72 0.12 245` | Light accent, grafy |
| primary-300 | `#9DC5FA` | `0.81 0.08 240` | Progress bary, light bg |
| primary-200 | `#C5DCFC` | `0.89 0.05 240` | Selected row, active sidebar |
| primary-100 | `#E7F3FF` | `0.95 0.02 240` | Hover bg, jemne zvyrazneni |
| primary-50 | `#F0F6FF` | `0.97 0.01 240` | Nejjemnejsi blue tint |

### 1.2 Neutral (Meta gray skala -- presne)

| Token | HEX | OKLCH | Pouziti |
|---|---|---|---|
| neutral-950 | `#1C1E21` | `0.18 0.005 260` | Primary text |
| neutral-900 | `#242628` | `0.22 0.005 260` | Dark mode bg |
| neutral-800 | `#373A3F` | `0.30 0.005 260` | Silny text, nadpisy |
| neutral-700 | `#4B4F54` | `0.38 0.005 260` | Dark mode secondary text |
| neutral-600 | `#65676B` | `0.47 0.005 260` | Secondary text (Meta) |
| neutral-500 | `#8A8D91` | `0.60 0.005 260` | Placeholder, disabled |
| neutral-400 | `#ADB1B5` | `0.73 0.005 260` | Disabled icons |
| neutral-300 | `#CED0D4` | `0.84 0.005 260` | Borders, dividers (Meta) |
| neutral-200 | `#E4E6EA` | `0.91 0.005 260` | Input borders |
| neutral-100 | `#F0F2F5` | `0.95 0.005 260` | Page background (Meta) |
| neutral-50 | `#F7F8FA` | `0.97 0.003 260` | Subtle bg |
| white | `#FFFFFF` | `1.00 0 0` | Karty, sidebar, modaly |

### 1.3 Semanticke barvy

| Token | HEX | OKLCH | Pouziti |
|---|---|---|---|
| success-700 | `#1B7D36` | `0.48 0.15 150` | Text na success bg |
| success-500 | `#31A24C` | `0.60 0.16 150` | Ikony, badges (Meta green) |
| success-50 | `#E6F5EA` | `0.95 0.04 150` | Background |
| warning-700 | `#B06D00` | `0.53 0.14 70` | Text na warning bg |
| warning-500 | `#F7B928` | `0.78 0.16 80` | Ikony, badges |
| warning-50 | `#FFF8E6` | `0.97 0.03 85` | Background |
| error-700 | `#B32D1B` | `0.43 0.16 25` | Text na error bg |
| error-500 | `#E34234` | `0.55 0.20 25` | Ikony, badges |
| error-50 | `#FDE8E8` | `0.95 0.04 15` | Background |

### 1.4 Brand akcenty (pouzivat strídme)

| Token | HEX | OKLCH | Pouziti |
|---|---|---|---|
| brand-gold | `#D4A853` | `0.74 0.12 70` | Premium/enterprise badge |
| brand-teal | `#4ECDC4` | `0.77 0.10 185` | Status "running" (agent aktivni) |
| brand-navy | `#1A3C5E` | `0.29 0.06 240` | Logo, brand mark |

---

## 2. CSS promenne (Tailwind v4 + shadcn/ui)

Tailwind v4 pouziva `@theme inline` -- barvy se definuji primo v CSS,
ne v tailwind.config.ts. shadcn/ui ocekava oklch format.

**Source of truth:** `app/globals.css`
Vygenerovano pres [tweakcn.com](https://tweakcn.com) (Meta Business Suite inspired preset).
Obsahuje kompletni light + dark mode, shadows, tracking, `@theme inline` mapovani.

---

## 3. Typografie

```tsx
// app/layout.tsx
import { Inter, JetBrains_Mono } from "next/font/google";

const inter = Inter({ subsets: ["latin"], variable: "--font-sans" });
const jetbrainsMono = JetBrains_Mono({ subsets: ["latin"], variable: "--font-mono" });
```

| Ucel | Tailwind trida | Skutecna velikost |
|---|---|---|
| Velke cislo (dashboard) | `text-3xl font-bold` | 30px / 700 |
| H1 (nazev stranky) | `text-2xl font-semibold` | 24px / 600 |
| H2 (sekce) | `text-xl font-semibold` | 20px / 600 |
| H3 (karta) | `text-lg font-medium` | 18px / 500 |
| Body (zakladni text) | `text-sm` | 14px / 400 |
| Caption (popisky) | `text-xs text-muted-foreground` | 12px / 400 |
| Code (logy, terminal) | `text-sm font-mono` | 14px mono / 400 |

> Base size = 14px (`text-sm`), ne 16px. Stejne jako Meta -- hustejsi, profesionalnejsi.

---

## 4. Ikony (Lucide React)

**Jedina povolena ikonova knihovna: `lucide-react`**

```tsx
import { LayoutDashboard, Users, Bot, Puzzle, KeyRound, ScrollText, Settings, BookOpen, MessageCircle, Ship } from "lucide-react";
```

### Sidebar navigace -- mapovani ikon

| Polozka | Lucide ikona | Import |
|---|---|---|
| Dashboard | `LayoutDashboard` | `LayoutDashboard` |
| Teams | `Users` | `Users` |
| Agents | `Bot` | `Bot` |
| Skills | `Puzzle` | `Puzzle` |
| Credentials | `KeyRound` | `KeyRound` |
| Audit Log | `ScrollText` | `ScrollText` |
| Settings | `Settings` | `Settings` |
| Docs | `BookOpen` | `BookOpen` |
| Support | `MessageCircle` | `MessageCircle` |
| Logo | `Ship` | `Ship` |

### Velikost ikon

| Kontext | Tailwind trida | Velikost |
|---|---|---|
| Sidebar item | `size-5` | 20px |
| Inline s textem | `size-4` | 16px |
| Page header | `size-6` | 24px |
| Empty state | `size-12` | 48px |
| Button icon | `size-4` | 16px |

### Status badges

| Stav | Barva | Lucide ikona |
|---|---|---|
| Running | `bg-success/10 text-success-700` | `CircleDot` (pulsing) |
| Idle | `bg-primary-100 text-primary-700` | `Circle` |
| Starting | `bg-warning/10 text-warning-700` | `Loader2` (animate-spin) |
| Stopped | `bg-neutral-200 text-neutral-600` | `Square` |
| Error | `bg-error/10 text-error-700` | `AlertTriangle` |

---

## 5. Layout

### Struktura

```
+-------------------+--------------------------------------+
| Sidebar (256px)   | Main Content                         |
|                   |                                      |
| [Ship] Crewship   | Breadcrumb / Page header             |
|                   |                                      |
| Dashboard         | +------------+ +------------+        |
| Teams             | |   Card     | |   Card     |        |
|   > Marketing     | |            | |            |        |
|   > DevOps        | +------------+ +------------+        |
| Agents            |                                      |
| Skills            | +--------------------------------+   |
| Credentials       | |  Full-width card               |   |
| Audit Log         | |  (tabulka, chat, logy)         |   |
|                   | +--------------------------------+   |
| ────────          |                                      |
| Settings          |                                      |
| Docs              |                                      |
+-------------------+--------------------------------------+
```

### Spacing

| Prvek | Tailwind |
|---|---|
| Page padding | `p-6` (24px) |
| Card padding | `p-6` (24px) |
| Mezera mezi kartami | `gap-4` (16px) |
| Mezera mezi sekcemi | `gap-8` (32px) |
| Sidebar sirka | `w-64` (256px) |
| Sidebar item padding | `px-3 py-2` |

### Karty

- `bg-card border border-border rounded-lg`
- **Zadne stiny** (flat design, jako Meta)
- Header oddeleny `border-b`

---

## 6. Dark mode

Light mode = **default**. Dark mode pres `next-themes` + toggle v Settings.

| Prvek | Light | Dark |
|---|---|---|
| Page bg | neutral-100 `#F0F2F5` | neutral-950 `#1C1E21` |
| Card bg | white `#FFFFFF` | neutral-900 `#242628` |
| Sidebar bg | white | neutral-900 |
| Text | neutral-950 `#1C1E21` | neutral-200 `#E4E6EA` |
| Borders | neutral-300 `#CED0D4` | neutral-800 `#373A3F` |
| Primary | primary-600 `#1877F2` | primary-500 `#4293F5` |

---

## 7. Migrace z Tailwind v3

Soucasny projekt pouziva Tailwind v3 s `tailwind.config.ts` a `oklch(var(--xxx) / <alpha-value>)`.
Pri scaffoldingu (Tyden 1) se musi:

1. `pnpm remove tailwindcss postcss autoprefixer tailwindcss-animate @tailwindcss/typography`
2. `pnpm add -D tailwindcss@^4.1 @tailwindcss/postcss tw-animate-css`
3. Smazat `tailwind.config.ts` a `postcss.config.cjs`
4. Vytvorit `postcss.config.mjs`: `export default { plugins: { "@tailwindcss/postcss": {} } }`
5. Nahradit `app/globals.css` obsahem ze sekce 2 vyse
6. Aktualizovat `components.json` pro shadcn/ui v4

### components.json (Tailwind v4)

```json
{
  "$schema": "https://ui.shadcn.com/schema.json",
  "style": "new-york",
  "rsc": true,
  "tsx": true,
  "tailwind": {
    "config": "",
    "css": "app/globals.css",
    "baseColor": "slate",
    "cssVariables": true,
    "prefix": ""
  },
  "aliases": {
    "components": "@/components",
    "utils": "@/lib/utils",
    "ui": "@/components/ui",
    "hooks": "@/hooks",
    "lib": "@/lib"
  }
}
```
