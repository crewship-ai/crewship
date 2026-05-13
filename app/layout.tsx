import type { Metadata } from "next"
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
