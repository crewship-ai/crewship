"use client"

import * as React from "react"
import { Bot, Mail, Plug } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * Brand marks for the services this instance can connect to.
 *
 * Why the artwork is vendored rather than pulled from a component library:
 * `react-icons/si` dropped several of these in 5.7.0 after trademark
 * takedowns, and the ones it keeps are monochrome silhouettes. A catalog of
 * eleven grey glyphs is not recognisable — you find Slack by its colour long
 * before you read the label — so the full-colour mark is the point, not a
 * decoration.
 *
 * Sources and licences — these differ, so keep the per-mark attribution:
 *   · SVG Logos (github.com/gilbarbara/logos) — CC0. Full-colour marks;
 *     rendered verbatim, so they carry their own fills and are never tinted.
 *   · Simple Icons — CC0. Monochrome; tinted with the brand's official hex.
 *   · Tabler Icons — MIT. Monochrome; same treatment.
 *   · Arcticons (github.com/Donnnno/Arcticons) — CC BY-SA 4.0. Used for the
 *     Gotify mark only, which exists nowhere under a more permissive licence.
 *     SHARE-ALIKE: that one glyph carries a copyleft obligation the rest of
 *     this repo does not. It is called out here rather than blended into the
 *     list so a future reader does not assume the whole file is CC0/MIT.
 */

interface BrandMark {
  label: string
  /** The brand's own hex — also the tile colour for the lettermark fallback. */
  color: string
  /**
   * true = the artwork carries its own fills (CC0 SVG Logos) and must be
   * rendered untouched. false = a monochrome silhouette we tint with `color`.
   */
  fullColour: boolean
  /**
   * true = the artwork is drawn with strokes, not fills. Forcing
   * fill="currentColor" onto one of these floods the glyph into a solid blob,
   * so the renderer must leave `fill` to the paths and tint via `color`.
   */
  stroke?: boolean
  viewBox: string
  body: React.ReactNode
}

const BRANDS: Record<string, BrandMark> = {
  discord: {
    label: "Discord",
    color: "#5865F2",
    fullColour: true,
    viewBox: "0 0 256 199",
    body: (
      <><path fill="#5865f2" d="M216.856 16.597A208.5 208.5 0 0 0 164.042 0c-2.275 4.113-4.933 9.645-6.766 14.046q-29.538-4.442-58.533 0c-1.832-4.4-4.55-9.933-6.846-14.046a207.8 207.8 0 0 0-52.855 16.638C5.618 67.147-3.443 116.4 1.087 164.956c22.169 16.555 43.653 26.612 64.775 33.193A161 161 0 0 0 79.735 175.3a136.4 136.4 0 0 1-21.846-10.632a109 109 0 0 0 5.356-4.237c42.122 19.702 87.89 19.702 129.51 0a132 132 0 0 0 5.355 4.237a136 136 0 0 1-21.886 10.653c4.006 8.02 8.638 15.67 13.873 22.848c21.142-6.58 42.646-16.637 64.815-33.213c5.316-56.288-9.08-105.09-38.056-148.36M85.474 135.095c-12.645 0-23.015-11.805-23.015-26.18s10.149-26.2 23.015-26.2s23.236 11.804 23.015 26.2c.02 14.375-10.148 26.18-23.015 26.18m85.051 0c-12.645 0-23.014-11.805-23.014-26.18s10.148-26.2 23.014-26.2c12.867 0 23.236 11.804 23.015 26.2c0 14.375-10.148 26.18-23.015 26.18"/></>
    ),
  },
  slack: {
    label: "Slack",
    color: "#4A154B",
    fullColour: true,
    viewBox: "0 0 256 256",
    body: (
      <><path fill="#e01e5a" d="M53.841 161.32c0 14.832-11.987 26.82-26.819 26.82S.203 176.152.203 161.32c0-14.831 11.987-26.818 26.82-26.818H53.84zm13.41 0c0-14.831 11.987-26.818 26.819-26.818s26.819 11.987 26.819 26.819v67.047c0 14.832-11.987 26.82-26.82 26.82c-14.83 0-26.818-11.988-26.818-26.82z"/><path fill="#36c5f0" d="M94.07 53.638c-14.832 0-26.82-11.987-26.82-26.819S79.239 0 94.07 0s26.819 11.987 26.819 26.819v26.82zm0 13.613c14.832 0 26.819 11.987 26.819 26.819s-11.987 26.819-26.82 26.819H26.82C11.987 120.889 0 108.902 0 94.069c0-14.83 11.987-26.818 26.819-26.818z"/><path fill="#2eb67d" d="M201.55 94.07c0-14.832 11.987-26.82 26.818-26.82s26.82 11.988 26.82 26.82s-11.988 26.819-26.82 26.819H201.55zm-13.41 0c0 14.832-11.988 26.819-26.82 26.819c-14.831 0-26.818-11.987-26.818-26.82V26.82C134.502 11.987 146.489 0 161.32 0s26.819 11.987 26.819 26.819z"/><path fill="#ecb22e" d="M161.32 201.55c14.832 0 26.82 11.987 26.82 26.818s-11.988 26.82-26.82 26.82c-14.831 0-26.818-11.988-26.818-26.82V201.55zm0-13.41c-14.831 0-26.818-11.988-26.818-26.82c0-14.831 11.987-26.818 26.819-26.818h67.25c14.832 0 26.82 11.987 26.82 26.819s-11.988 26.819-26.82 26.819z"/></>
    ),
  },
  telegram: {
    label: "Telegram",
    color: "#26A5E4",
    fullColour: true,
    viewBox: "0 0 256 256",
    body: (
      <><defs><linearGradient id="SVG6DaOZcwt" x1="50%" x2="50%" y1="0%" y2="100%"><stop offset="0%" stopColor="#2aabee"/><stop offset="100%" stopColor="#229ed9"/></linearGradient></defs><path fill="url(#SVG6DaOZcwt)" d="M128 0C94.06 0 61.48 13.494 37.5 37.49A128.04 128.04 0 0 0 0 128c0 33.934 13.5 66.514 37.5 90.51C61.48 242.506 94.06 256 128 256s66.52-13.494 90.5-37.49c24-23.996 37.5-56.576 37.5-90.51s-13.5-66.514-37.5-90.51C194.52 13.494 161.94 0 128 0"/><path fill="#fff" d="M57.94 126.648q55.98-24.384 74.64-32.152c35.56-14.786 42.94-17.354 47.76-17.441c1.06-.017 3.42.245 4.96 1.49c1.28 1.05 1.64 2.47 1.82 3.467c.16.996.38 3.266.2 5.038c-1.92 20.24-10.26 69.356-14.5 92.026c-1.78 9.592-5.32 12.808-8.74 13.122c-7.44.684-13.08-4.912-20.28-9.63c-11.26-7.386-17.62-11.982-28.56-19.188c-12.64-8.328-4.44-12.906 2.76-20.386c1.88-1.958 34.64-31.748 35.26-34.45c.08-.338.16-1.598-.6-2.262c-.74-.666-1.84-.438-2.64-.258c-1.14.256-19.12 12.152-54 35.686c-5.1 3.508-9.72 5.218-13.88 5.128c-4.56-.098-13.36-2.584-19.9-4.708c-8-2.606-14.38-3.984-13.82-8.41c.28-2.304 3.46-4.662 9.52-7.072"/></>
    ),
  },
  mattermost: {
    label: "Mattermost",
    color: "#0058CC",
    fullColour: true,
    viewBox: "0 0 256 256",
    body: (
      <><path fill="#0058cc" d="M6.791 86.965C25.235 32.482 76.783-1.432 131.421.046L113.91 20.74C81.496 26.6 53.507 48.735 42.507 81.23c-16.366 48.347 11.066 101.317 61.272 118.315c50.207 16.994 104.174-8.421 120.54-56.766c10.965-32.387 2.27-66.847-19.756-91.18l-1.346-27.169c44.154 32.048 64.406 90.205 45.991 144.6c-22.662 66.941-95.298 102.837-162.24 80.176c-66.94-22.662-102.837-95.299-80.177-162.24m158.394-75.041a2.96 2.96 0 0 1 2.137-.098a2.97 2.97 0 0 1 1.614 1.334l.072.116l.064.134c.168.321.311.69.378 1.141c.132.89.192 2.985.216 5.13l.005.585c.006.683.009 1.36.01 1.994v.532c-.002 1.735-.017 3.035-.017 3.035l.503 18.933l.744 21.855l.927 37.98v.083l.001.045v.121c-.007 2.17-.452 18.049-11.717 29.085c-12.112 11.866-26.99 10.78-36.67 7.504c-9.68-3.278-22.158-11.453-24.572-28.237c-2.052-14.266 5.533-26.257 7.854-29.533l.155-.217c.316-.438.5-.668.5-.668l23.808-29.606l13.868-16.91l11.9-14.734s1.75-2.345 3.551-4.653l.36-.46a111 111 0 0 1 1.718-2.141l.305-.366c.444-.527.82-.952 1.085-1.208c.308-.3.625-.494.935-.645l.227-.116Z"/></>
    ),
  },
  teams: {
    label: "Microsoft Teams",
    color: "#6264A7",
    fullColour: true,
    viewBox: "0 0 256 239",
    body: (
      <><defs><linearGradient id="SVGCo9xpmZW" x1="17.372%" x2="82.628%" y1="-6.51%" y2="106.51%"><stop offset="0%" stopColor="#5a62c3"/><stop offset="50%" stopColor="#4d55bd"/><stop offset="100%" stopColor="#3940ab"/></linearGradient><path id="SVGhJCUgeMn" d="M136.93 64.476v12.8a32.7 32.7 0 0 1-5.953-.952a38.7 38.7 0 0 1-26.79-22.742h21.848c6.008.022 10.872 4.887 10.895 10.894"/></defs><path fill="#5059c9" d="M178.563 89.302h66.125c6.248 0 11.312 5.065 11.312 11.312v60.231c0 22.96-18.613 41.574-41.573 41.574h-.197c-22.96.003-41.576-18.607-41.579-41.568V95.215a5.91 5.91 0 0 1 5.912-5.913"/><circle cx="223.256" cy="50.605" r="26.791" fill="#5059c9"/><circle cx="139.907" cy="38.698" r="38.698" fill="#7b83eb"/><path fill="#7b83eb" d="M191.506 89.302H82.355c-6.173.153-11.056 5.276-10.913 11.449v68.697c-.862 37.044 28.445 67.785 65.488 68.692c37.043-.907 66.35-31.648 65.489-68.692v-68.697c.143-6.173-4.74-11.296-10.913-11.449"/><path d="M142.884 89.302v96.268a10.96 10.96 0 0 1-6.787 10.062c-1.3.55-2.697.833-4.108.833H76.68c-.774-1.965-1.488-3.93-2.084-5.953a72.5 72.5 0 0 1-3.155-21.076v-68.703c-.143-6.163 4.732-11.278 10.895-11.43z" opacity=".1"/><path d="M136.93 89.302v102.222c0 1.411-.283 2.808-.833 4.108a10.96 10.96 0 0 1-10.062 6.787H79.48c-1.012-1.965-1.965-3.93-2.798-5.954a59 59 0 0 1-2.084-5.953a72.5 72.5 0 0 1-3.155-21.076v-68.703c-.143-6.163 4.732-11.278 10.895-11.43z" opacity=".2"/><path d="M136.93 89.302v90.315c-.045 5.998-4.896 10.85-10.895 10.895H74.597a72.5 72.5 0 0 1-3.155-21.076v-68.703c-.143-6.163 4.732-11.278 10.895-11.43z" opacity=".2"/><path d="M130.977 89.302v90.315c-.046 5.998-4.897 10.85-10.895 10.895H74.597a72.5 72.5 0 0 1-3.155-21.076v-68.703c-.143-6.163 4.732-11.278 10.895-11.43z" opacity=".2"/><path d="M142.884 58.523v18.753c-1.012.06-1.965.12-2.977.12s-1.965-.06-2.977-.12a32.7 32.7 0 0 1-5.953-.952a38.7 38.7 0 0 1-26.791-22.742a33 33 0 0 1-1.905-5.954h29.708c6.007.023 10.872 4.887 10.895 10.895" opacity=".1"/><use href="#SVGhJCUgeMn" opacity=".2"/><use href="#SVGhJCUgeMn" opacity=".2"/><path d="M130.977 64.476v11.848a38.7 38.7 0 0 1-26.791-22.743h15.896c6.008.023 10.872 4.888 10.895 10.895" opacity=".2"/><path fill="url(#SVGCo9xpmZW)" d="M10.913 53.581h109.15c6.028 0 10.914 4.886 10.914 10.913v109.151c0 6.027-4.886 10.913-10.913 10.913H10.913C4.886 184.558 0 179.672 0 173.645V64.495C0 58.466 4.886 53.58 10.913 53.58"/><path fill="#fff" d="M94.208 95.125h-21.82v59.416H58.487V95.125H36.769V83.599h57.439z"/></>
    ),
  },
  opsgenie: {
    label: "Opsgenie",
    color: "#172B4D",
    fullColour: true,
    viewBox: "0 0 256 305",
    body: (
      <><defs><linearGradient id="SVGeY8NEnFa" x1="50%" x2="50%" y1="16.62%" y2="119.283%"><stop offset="0%" stopColor="#2684ff"/><stop offset="82%" stopColor="#0052cc"/></linearGradient><linearGradient id="SVGoXiXXbjr" x1="41.18%" x2="67.714%" y1="31.16%" y2="78.678%"><stop offset="0%" stopColor="#2684ff"/><stop offset="62%" stopColor="#0052cc"/></linearGradient></defs><circle cx="127.996" cy="76.058" r="76.058" fill="url(#SVGeY8NEnFa)"/><path fill="url(#SVGoXiXXbjr)" d="M121.516 302.953A366.9 366.9 0 0 1 1.076 177.056a8.527 8.527 0 0 1 3.71-11.81l57.597-28.265a8.527 8.527 0 0 1 11.128 3.41a284.75 284.75 0 0 0 123.636 111.913a368.8 368.8 0 0 1-62.67 50.649a12.24 12.24 0 0 1-12.961 0"/><path fill="#2684ff" d="M134.476 302.953a366.65 366.65 0 0 0 120.44-125.897a8.527 8.527 0 0 0-3.667-11.81l-57.64-28.265a8.527 8.527 0 0 0-11.127 3.41A284.6 284.6 0 0 1 58.845 252.305a366.7 366.7 0 0 0 62.67 50.649a12.24 12.24 0 0 0 12.961 0"/></>
    ),
  },
  gotify: {
    label: "Gotify",
    color: "#71CAEE",
    fullColour: false,
    stroke: true,
    viewBox: "0 0 48 48",
    body: (
      <><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="M32.632 35.406c-1.59 2.423-3.29 5.035-7.064 6.175c-3.782 1.13-9.758.967-13.407-.719c-3.658-1.695-4.99-4.922-4.31-7.422c.67-2.508 3.343-4.3 4.55-6.224c1.197-1.935.929-4.003.613-5.918a28 28 0 0 1-.613-5.095a10.3 10.3 0 0 1 1.029-3.737"/><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="M14.555 12.612c-6.943-.48-1.436-6.704.958-1.916"/><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="M15.181 10.13c4.55-5.266 21.4-6.74 23.076 6.666"/><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="M19.822 7.273c.185-1.999 1.217-2.154 2.616-.672m15.819 10.196a1.214 1.214 0 0 1 1.312 1.082a1.345 1.345 0 0 1-2.633 0a1.217 1.217 0 0 1 1.321-1.082"/><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="M37.06 18.358c-5.507 3.954 5.381 4.123 2.509-.386M28.68 11.175a5.746 5.746 0 1 1-5.745 5.746a5.744 5.744 0 0 1 5.746-5.746"/><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="M30.596 15.724a1.323 1.323 0 1 1-1.197 1.312a1.257 1.257 0 0 1 1.197-1.312m7.901 5.424v1.785M36.29 11.294c2.227-.738 4.476 2.871 3.03 5.951m-12.392 8.209l13.14-2.864a1.4 1.4 0 0 1 1.664 1.067l.001.006l1.734 7.948a1.4 1.4 0 0 1-1.063 1.666l-13.149 2.864a1.4 1.4 0 0 1-1.665-1.066l-.001-.006l-1.733-7.949a1.4 1.4 0 0 1 1.066-1.665ZM8.884 31.378c-5.77-2.452-1.072-5.023.829-1.03M4.5 37.99q3.112 2.872 5.028-.719m21.454.497c1.676-.389 2.225.73 1.53 3.334"/><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="M23.174 26.498q1.197 2.288 3.83.134"/><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" d="m26.047 26.976l9.098 3.965l6.294-7.882M28.479 36.09l4.74-5.988m3.341-.932l6.691 3.508"/></>
    ),
  },
  webhook: {
    label: "Webhook",
    color: "#C73A63",
    fullColour: false,
    stroke: true,
    viewBox: "0 0 24 24",
    body: (
      <><g fill="none" stroke="currentColor" strokeWidth="1.5"><path strokeLinecap="round" strokeLinejoin="round" d="M5.062 13A4 4 0 1 0 11 16.5h6"/><path strokeLinecap="round" strokeLinejoin="round" d="m12 7.5l3.057 5.503a4 4 0 1 1-.557 6.62"/><path d="M12 8.5a1 1 0 1 0 0-2m0 2a1 1 0 1 1 0-2m0 2v-2m-5 11a1 1 0 1 0 0-2m0 2a1 1 0 1 1 0-2m0 2v-2m10 2a1 1 0 1 0 0-2m0 2a1 1 0 1 1 0-2m0 2v-2"/><path strokeLinecap="round" strokeLinejoin="round" d="M16 7.5a4 4 0 1 0-5.943 3.497L7 16.5"/></g></>
    ),
  },
  ntfy: {
    label: "ntfy",
    color: "#317F6F",
    fullColour: false,
    viewBox: "0 0 24 24",
    body: (
      <><path fill="currentColor" d="M12.597 13.693v2.156h6.205v-2.156ZM5.183 6.549v2.363l3.591 1.901l.023.01l-.023.009l-3.591 1.901v2.35l.386-.211l5.456-2.969V9.729ZM3.659 2.037C1.915 2.037.42 3.41.42 5.154v.002L.438 18.73L0 21.963l5.956-1.583h14.806c1.744 0 3.238-1.374 3.238-3.118V5.154c0-1.744-1.493-3.116-3.237-3.117h-.001zm0 2.2h17.104c.613.001 1.037.447 1.037.917v12.108c0 .47-.424.916-1.038.916H5.633l-3.026.915l.031-.179l-.017-13.76c0-.47.424-.917 1.038-.917"/></>
    ),
  },
  matrix: {
    label: "Matrix",
    color: "#000000",
    fullColour: false,
    viewBox: "0 0 24 24",
    body: (
      <><path fill="currentColor" d="M.632.55v22.9H2.28V24H0V0h2.28v.55zm7.043 7.26v1.157h.033a3.3 3.3 0 0 1 1.117-1.024c.433-.245.936-.365 1.5-.365q.81.002 1.481.314c.448.208.785.582 1.02 1.108q.382-.562 1.034-.992q.651-.43 1.546-.43q.679 0 1.26.167c.388.11.716.286.993.53c.276.245.489.559.646.951q.229.587.23 1.417v5.728h-2.349V11.52q0-.43-.032-.812a1.8 1.8 0 0 0-.18-.66a1.1 1.1 0 0 0-.438-.448q-.292-.165-.785-.166q-.498 0-.803.189a1.4 1.4 0 0 0-.48.499a2 2 0 0 0-.231.696a6 6 0 0 0-.06.785v4.768h-2.35v-4.8q.002-.38-.018-.752a2.1 2.1 0 0 0-.143-.688a1.05 1.05 0 0 0-.415-.503c-.194-.125-.476-.19-.854-.19q-.168 0-.439.074c-.18.051-.36.143-.53.282a1.64 1.64 0 0 0-.439.595q-.18.39-.18 1.02v4.966H5.46V7.81zm15.693 15.64V.55H21.72V0H24v24h-2.28v-.55z"/></>
    ),
  },
  googlechat: {
    label: "Google Chat",
    color: "#34A853",
    fullColour: false,
    viewBox: "0 0 24 24",
    body: (
      <><path fill="currentColor" d="M1.637 0C.733 0 0 .733 0 1.637v16.5c0 .904.733 1.636 1.637 1.636h3.955v3.323c0 .804.97 1.207 1.539.638l3.963-3.96h11.27c.903 0 1.636-.733 1.636-1.637V5.592L18.408 0Zm3.955 5.592h12.816v8.59H8.455l-2.863 2.863Z"/></>
    ),
  },
  pushover: {
    label: "Pushover",
    color: "#249DF1",
    fullColour: false,
    viewBox: "0 0 24 24",
    body: (
      <><path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M6.16 10.985C5.33 9.05 7.69 3 14.355 3C17.688 3 19 4.382 19 6.9c0 2.597-2.612 6.1-9 6.1m2.5-7L7 21"/></>
    ),
  },
}

/**
 * Brands with no usable mark. A lettermark in the brand's real colour is
 * honest — it says "we don't have their artwork" rather than dressing up a
 * generic glyph as their logo. Empty today; kept because the next provider
 * added to the Go catalog may well arrive without artwork.
 */
const LETTERMARKS: Record<string, { label: string; color: string }> = {}

/** Non-brand entries: our own transports and the managed-tools surface. */
const BUILTIN: Record<string, { label: string; color: string; Icon: typeof Mail }> = {
  email: { label: "E-mail", color: "#3B82F6", Icon: Mail },
  composio: { label: "Composio", color: "#8B5CF6", Icon: Bot },
}

/**
 * Relative luminance (Rec. 709). Good enough for the two-way choices below.
 */
function luma(hex: string): number {
  const h = hex.replace("#", "")
  const r = parseInt(h.slice(0, 2), 16)
  const g = parseInt(h.slice(2, 4), 16)
  const b = parseInt(h.slice(4, 6), 16)
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255
}

/**
 * The colour a monochrome mark is actually drawn in.
 *
 * Some official brand colours are black — Matrix is #000000 — and the app is
 * dark-first, so drawing them faithfully means drawing them invisible. Matrix's
 * own brand guidance is white-on-dark for exactly this reason, so a near-black
 * silhouette is lifted rather than rendered as a smudge. Everything else keeps
 * its real hex, which is the whole point of vendoring the artwork.
 */
function glyphColour(brand: BrandMark): string {
  return luma(brand.color) < 0.12 ? "#E8E8EC" : brand.color
}

/**
 * The tile's tint. Derived from the same lift, so a near-black brand does not
 * get an invisible tile under a visible mark.
 */
function tintColour(brand: BrandMark): string {
  return luma(brand.color) < 0.12 ? "#9AA0AA" : brand.color
}

/**
 * Foreground that stays legible on `hex`.
 *
 * Needed because the tiles span nearly the full lightness range — Opsgenie is
 * #172B4D, Gotify is #71CAEE — and a single hardcoded white would be
 * unreadable on half of them.
 */
function readableOn(hex: string): string {
  return luma(hex) > 0.6 ? "#0B0B0F" : "#FFFFFF"
}

/** Does this service have artwork (as opposed to a lettermark)? */
export function hasBrandMark(provider: string): boolean {
  return provider in BRANDS
}

/** The brand colour for a service, for dots and accents. */
export function brandColor(provider: string): string | undefined {
  return BRANDS[provider]?.color ?? LETTERMARKS[provider]?.color ?? BUILTIN[provider]?.color
}

/**
 * The colour a service is actually DRAWN in — brandColor with the near-black
 * lift applied. They differ only for Matrix today; asserting on this is what
 * catches "faithful to the brand, invisible to the user".
 */
export function displayColor(provider: string): string | undefined {
  const brand = BRANDS[provider]
  if (brand) return brand.fullColour ? brand.color : glyphColour(brand)
  return LETTERMARKS[provider]?.color ?? BUILTIN[provider]?.color
}

function initials(label: string): string {
  const words = label.replace(/[^A-Za-z0-9 ]/g, "").trim().split(/\s+/).filter(Boolean)
  if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase()
  return (words[0] ?? "?").slice(0, 2).toUpperCase()
}

export interface ProviderMarkProps {
  /** Provider key: discord | slack | … | email | webhook | composio. */
  provider: string
  /** Fallback label for the lettermark when the provider is unknown. */
  label?: string
  /**
   * A remote logo to prefer over everything vendored here.
   *
   * Composio's catalog is 1000+ apps and it serves artwork for all of them, so
   * gmail, googledrive and youtube cannot come from the eleven marks bundled in
   * this file. Passing the toolkit's logo is what stops those rows falling back
   * to two-letter tiles while the main column beside them shows real icons.
   * A 404 degrades to the vendored mark, then to a lettermark.
   */
  logoUrl?: string
  /** Tailwind size classes for the tile. */
  className?: string
  /** Skip the tile and render the bare glyph (table rows, dense lists). */
  bare?: boolean
}

/**
 * A service's mark.
 *
 * Full-colour brands render on a tinted tile so that Slack's aubergine and
 * Discord's blurple sit on comparable surfaces instead of one vanishing into
 * the page background. Monochrome brands are tinted with their own hex.
 */
export function ProviderMark({
  provider,
  label,
  logoUrl,
  className,
  bare,
}: ProviderMarkProps) {
  const key = provider.toLowerCase()
  const brand = BRANDS[key]
  const builtin = BUILTIN[key]
  const letter = LETTERMARKS[key]

  // Reset when the source changes, or a re-used instance keeps showing the
  // fallback it fell back to for a *different* logo.
  const [remoteFailed, setRemoteFailed] = React.useState(false)
  React.useEffect(() => setRemoteFailed(false), [logoUrl])

  if (logoUrl && !remoteFailed) {
    // A plain <img>, not next/image: next/image chokes on remote SVGs under
    // static export, and the rest of the Composio surface renders toolkit
    // logos the same way for the same reason.
    const img = (
      <img
        src={logoUrl}
        alt=""
        className={cn("object-contain", bare ? "h-full w-full" : "h-[62%] w-[62%] rounded-sm")}
        onError={() => setRemoteFailed(true)}
      />
    )
    if (bare) return img
    return (
      <span
        data-provider-mark={key}
        className={cn(
          "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md",
          "bg-white/[0.06] shadow-[inset_0_0_0_1px_rgba(255,255,255,0.07)]",
          className,
        )}
      >
        {img}
      </span>
    )
  }

  if (brand) {
    const glyph = (
      <svg
        viewBox={brand.viewBox}
        role="img"
        aria-label={brand.label}
        className={cn("h-[62%] w-[62%]", bare && "h-full w-full")}
        fill={brand.fullColour || brand.stroke ? undefined : "currentColor"}
        style={brand.fullColour ? undefined : { color: glyphColour(brand) }}
      >
        {brand.body}
      </svg>
    )
    if (bare) return glyph
    return (
      <span
        data-provider-mark={key}
        className={cn(
          "inline-flex shrink-0 items-center justify-center rounded-md",
          "h-7 w-7",
          className,
        )}
        // A very dark mark (Opsgenie, Matrix) disappears on our background, so
        // the tile is tinted with the brand colour at low alpha and a hairline
        // of the same hue — enough separation without inventing a new colour.
        style={{
          backgroundColor: `color-mix(in oklab, ${tintColour(brand)} 18%, transparent)`,
          boxShadow: `inset 0 0 0 1px color-mix(in oklab, ${tintColour(brand)} 35%, transparent)`,
        }}
      >
        {glyph}
      </span>
    )
  }

  if (builtin) {
    const Icon = builtin.Icon
    if (bare) return <Icon className="h-full w-full" style={{ color: builtin.color }} />
    return (
      <span
        data-provider-mark={key}
        className={cn("inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md", className)}
        style={{
          backgroundColor: `color-mix(in oklab, ${builtin.color} 18%, transparent)`,
          boxShadow: `inset 0 0 0 1px color-mix(in oklab, ${builtin.color} 35%, transparent)`,
        }}
      >
        <Icon className="h-[58%] w-[58%]" style={{ color: builtin.color }} aria-label={builtin.label} />
      </span>
    )
  }

  const text = letter?.label ?? label ?? provider
  const colour = letter?.color ?? "#6B7280"
  if (bare) return <Plug className="h-full w-full" style={{ color: colour }} aria-label={text} />
  return (
    <span
      className={cn(
        "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[10px] font-bold",
        className,
      )}
      style={{ backgroundColor: colour, color: readableOn(colour) }}
      aria-label={text}
    >
      {initials(text)}
    </span>
  )
}
