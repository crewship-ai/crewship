import type { Metadata, Viewport } from "next"
import { Open_Sans } from "next/font/google"
import { Providers } from "@/components/providers"
import "./globals.css"

const openSans = Open_Sans({
  subsets: ["latin", "latin-ext"],
  variable: "--font-sans",
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
    <html lang="en" className="dark">
      <body className={`${openSans.variable} font-sans antialiased`}>
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
