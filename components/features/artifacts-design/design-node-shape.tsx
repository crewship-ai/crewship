import { safeImageSource, type DesignNode } from "./design-document"

/** Paint of one node at its local origin; shared by the editor canvas and prototype playback. */
export function DesignNodeShape({ node, width, height }: { node: DesignNode; width: number; height: number }) {
 const image = node.type === "image" ? safeImageSource(node.src) : undefined
 if (node.type === "text") return <><rect width={width} height={height} fill="transparent" /><text x="0" y="22" fill={node.fill || "#172554"} fontSize="20" fontFamily="system-ui, sans-serif">{String(node.text ?? "").split("\n").map((line, index) => <tspan key={index} x="0" dy={index ? "1.3em" : "0"}>{line}</tspan>)}</text></>
 if (node.type === "image") return image ? <image href={image} width={width} height={height} preserveAspectRatio="xMidYMid slice" /> : <><rect width={width} height={height} fill="#e2e8f0" rx="4" /><text x="12" y="28" fill="#64748b" fontSize="14">Set an image URL in Properties</text></>
 return <rect width={width} height={height} fill={node.fill || "#ffffff"} rx={node.type === "frame" ? 0 : 6} stroke={node.type === "frame" ? "#cbd5e1" : "none"} />
}
