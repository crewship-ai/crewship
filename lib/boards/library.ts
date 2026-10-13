import type { ExcalidrawElementSkeleton } from "@excalidraw/excalidraw/data/transform"

/** Original Crewship diagrams, licensed with the repository (Apache-2.0).
 * No community-library assets, SVG imports, URLs or remote icon services. */
export const BOARD_LIBRARY = [
  { id: "service", name: "Service", elements: [
    { type: "rectangle", x: 0, y: 0, width: 160, height: 100 },
    { type: "text", x: 38, y: 38, text: "Service", fontSize: 20 },
  ] },
  { id: "person", name: "Person", elements: [
    { type: "ellipse", x: 35, y: 0, width: 50, height: 50 },
    { type: "line", x: 60, y: 50, points: [[0, 0], [0, 55]] },
    { type: "line", x: 15, y: 75, points: [[0, 0], [90, 0]] },
    { type: "line", x: 60, y: 105, points: [[0, 0], [-35, 45]] },
    { type: "line", x: 60, y: 105, points: [[0, 0], [35, 45]] },
  ] },
  { id: "database", name: "Database", elements: [
    { type: "rectangle", x: 0, y: 18, width: 140, height: 100 },
    { type: "ellipse", x: 0, y: 0, width: 140, height: 36, backgroundColor: "#ffffff", fillStyle: "solid" },
    { type: "ellipse", x: 0, y: 100, width: 140, height: 36 },
  ] },
  { id: "browser", name: "Browser window", elements: [
    { type: "rectangle", x: 0, y: 0, width: 300, height: 200 },
    { type: "line", x: 0, y: 34, points: [[0, 0], [300, 0]] },
    { type: "ellipse", x: 12, y: 12, width: 10, height: 10 },
    { type: "ellipse", x: 30, y: 12, width: 10, height: 10 },
    { type: "ellipse", x: 48, y: 12, width: 10, height: 10 },
    { type: "rectangle", x: 85, y: 10, width: 190, height: 15 },
  ] },
  { id: "phone", name: "Phone wireframe", elements: [
    { type: "rectangle", x: 0, y: 0, width: 140, height: 260 },
    { type: "line", x: 45, y: 16, points: [[0, 0], [50, 0]] },
    { type: "rectangle", x: 10, y: 35, width: 120, height: 185 },
    { type: "ellipse", x: 60, y: 235, width: 20, height: 12 },
  ] },
  { id: "folder", name: "Folder", elements: [
    { type: "line", x: 0, y: 20, points: [[0, 0], [0, -20], [55, -20], [75, 0], [160, 0], [160, 100], [0, 100], [0, 0]] },
    { type: "line", x: 0, y: 38, points: [[0, 0], [160, 0]] },
  ] },
  { id: "decision", name: "Decision", elements: [
    { type: "diamond", x: 0, y: 0, width: 170, height: 120 },
    { type: "text", x: 38, y: 47, text: "Decision", fontSize: 20 },
  ] },
  { id: "note", name: "Note", elements: [
    { type: "rectangle", x: 0, y: 0, width: 180, height: 150, backgroundColor: "#fff3bf", fillStyle: "solid" },
    { type: "text", x: 20, y: 20, text: "Your note", fontSize: 20 },
  ] },
] satisfies { id: string; name: string; elements: ExcalidrawElementSkeleton[] }[]
