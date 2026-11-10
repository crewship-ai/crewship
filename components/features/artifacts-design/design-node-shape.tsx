import { useId } from "react"
import { designShapeStyle, designTextStyle, safeImageSource, type DesignNode } from "./design-document"

/** Paint of one node at its local origin; shared by the editor canvas and prototype playback. */
export function DesignNodeShape({ node, width, height }: { node: DesignNode; width: number; height: number }) {
 const clipId = `design-clip-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`
 const image = node.type === "image" ? safeImageSource(node.src) : undefined
 if (node.type === "text") return <DesignText node={node} width={width} height={height} clipId={clipId} />
 if (node.type === "frame") return <rect width={width} height={height} fill={node.fill || "#ffffff"} rx={0} stroke="#cbd5e1" />
 // Rectangles and images (#5219): the stroke is drawn inside the box, as the HTML export does.
 const shape = designShapeStyle(node)
 const inset = shape.stroke ? Math.min(shape.strokeWidth, width / 2, height / 2) : 0
 const outline = shape.stroke ? <rect data-shape-stroke="" x={inset / 2} y={inset / 2} width={Math.max(0, width - inset)} height={Math.max(0, height - inset)} rx={Math.max(0, shape.radius - inset / 2)} fill="none" stroke={shape.stroke} strokeWidth={inset} /> : null
 if (node.type === "image") {
  if (!image) return <><rect width={width} height={height} fill="#e2e8f0" rx={node.radius === undefined ? 4 : shape.radius} /><text x="12" y="28" fill="#64748b" fontSize="14">Set an image URL in Properties</text>{outline}</>
  if (!shape.radius) return <><image href={image} width={width} height={height} preserveAspectRatio="xMidYMid slice" />{outline}</>
  return <><clipPath id={clipId}><rect width={width} height={height} rx={shape.radius} /></clipPath><image href={image} width={width} height={height} preserveAspectRatio="xMidYMid slice" clipPath={`url(#${clipId})`} />{outline}</>
 }
 return <><rect width={width} height={height} fill={node.fill || "#ffffff"} rx={shape.radius} stroke="none" />{outline}</>
}

/**
 * A text layer. Without wrap, lines break only at a newline and are drawn as
 * SVG text, exactly as before text style existed. With wrap, the text flows
 * inside the box's width (long words break too). Clip cuts everything outside
 * the box; without it, text may run past the box as it always could.
 */
function DesignText({ node, width, height, clipId }: { node: DesignNode; width: number; height: number; clipId: string }) {
 const style = designTextStyle(node)
 const color = node.fill || "#172554"
 const text = String(node.text ?? "")
 const clip = style.clip ? <clipPath id={clipId}><rect width={width} height={height} /></clipPath> : null
 if (style.wrap) return <>
  <rect width={width} height={height} fill="transparent" />
  <foreignObject width={width} height={height} overflow={style.clip ? "hidden" : "visible"} data-text-layout="wrap">
   <div style={{ width, height: style.clip ? height : undefined, overflow: style.clip ? "hidden" : "visible", color, fontFamily: style.stack, fontSize: style.size, fontWeight: style.weight, lineHeight: style.lineHeight, textAlign: style.align, whiteSpace: "pre-wrap", overflowWrap: "anywhere", margin: 0 }}>{text}</div>
  </foreignObject>
 </>
 const x = style.align === "center" ? width / 2 : style.align === "right" ? width : 0
 const anchor = style.align === "center" ? "middle" : style.align === "right" ? "end" : undefined
 // The first baseline sits at 1.1 × the size (22 at the default 20 px).
 const baseline = Math.round(style.size * 11) / 10
 const styled = node.fontFamily !== undefined || node.fontWeight !== undefined || node.fontSize !== undefined || node.lineHeight !== undefined || node.textAlign !== undefined || style.clip
 if (!styled) return <><rect width={width} height={height} fill="transparent" /><text x="0" y="22" fill={color} fontSize="20" fontFamily="system-ui, sans-serif">{text.split("\n").map((line, index) => <tspan key={index} x="0" dy={index ? "1.3em" : "0"}>{line}</tspan>)}</text></>
 return <>
  {clip}
  <rect width={width} height={height} fill="transparent" />
  <text x={x} y={baseline} fill={color} fontSize={style.size} fontFamily={style.stack} fontWeight={style.weight} textAnchor={anchor} clipPath={style.clip ? `url(#${clipId})` : undefined} data-text-layout="lines">{text.split("\n").map((line, index) => <tspan key={index} x={x} dy={index ? `${style.lineHeight}em` : "0"}>{line}</tspan>)}</text>
 </>
}
