import type { Metadata, Viewport } from "next"
import localFont from "next/font/local"
import { Providers } from "@/components/providers"
import { HISTORY_NAVIGATION_GUARD_SCRIPT } from "@/lib/navigation-history-guard"
import { ACCENT_BOOT_SCRIPT } from "@/lib/theme/accents"
import { APPEARANCE_BOOT_SCRIPT } from "@/lib/theme/appearance"
import "./globals.css"

// Harbor 2 typography: Inter for the interface (one bundled variable file,
// weights 400–650; --font-sans in globals.css falls back to the platform
// face) and the bundled mono face for eyebrows, ids, chips and code. Both
// ship with the app; no font is fetched from a third party.
const inter = localFont({
  src: [{ path: "../public/fonts/inter/InterVariable.woff2", weight: "100 900", style: "normal" }],
  display: "swap",
  variable: "--font-inter",
})

const jetbrainsMono = localFont({
  src: [
    { path: "../public/fonts/jetbrains-mono/JetBrainsMono-Regular.woff2", weight: "400", style: "normal" },
    { path: "../public/fonts/jetbrains-mono/JetBrainsMono-Medium.woff2", weight: "500", style: "normal" },
    { path: "../public/fonts/jetbrains-mono/JetBrainsMono-SemiBold.woff2", weight: "600", style: "normal" },
  ],
  display: "swap",
  variable: "--font-jetbrains",
})

export const metadata: Metadata = {
  title: "Crewship",
  description:
    "Self-hosted runtime for AI coding agents — real Linux containers per crew, six CLI adapters in one workspace, journal-backed observability, cost budgets, and human-in-the-loop approvals.",
  icons: {
    // /icon.svg lives at app/icon.svg (Next.js Metadata convention).
    // Apple touch + shortcut entries share the same SVG — it has a navy
    // backdrop circle baked in, so it works on any home-screen color.
    icon: "/icon.svg",
    shortcut: "/icon.svg",
    apple: "/icon.svg",
  },
}

/**
 * Next's default viewport meta omits `viewport-fit`, which on iOS makes every
 * `env(safe-area-inset-*)` in the stylesheet evaluate to zero. The three places
 * that already asked for the inset — the create-flow footer, the save footer,
 * the phone nav sheet — were no-ops because of it (#2483).
 *
 * `maximumScale`/`userScalable` are deliberately left at their defaults:
 * pinch-zoom is an accessibility affordance, and the iOS focus-zoom this is
 * often used to suppress is already handled properly, by shipping 16px inputs
 * below `md` (components/ui/input.tsx).
 */
export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return (
    <html lang="en" className="dark" suppressHydrationWarning>
      <head>
        <script id="crewship-history-guard" dangerouslySetInnerHTML={{ __html: HISTORY_NAVIGATION_GUARD_SCRIPT }} />
        {/* Paints the stored accent theme before first paint (lib/theme/accents.ts). */}
        <script id="crewship-accent" dangerouslySetInnerHTML={{ __html: ACCENT_BOOT_SCRIPT }} />
        {/* Paints the stored density and reduce-motion choice the same way (lib/theme/appearance.ts). */}
        <script id="crewship-appearance" dangerouslySetInnerHTML={{ __html: APPEARANCE_BOOT_SCRIPT }} />
      </head>
      <body className={`${inter.variable} ${jetbrainsMono.variable} font-sans antialiased`}>
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
