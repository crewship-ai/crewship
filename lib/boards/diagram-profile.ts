const node = String.raw`([A-Za-z][A-Za-z0-9_]{0,63})(?:\[([\p{L}\p{N} .,_()\-]{1,200})\])?`
const declaration = new RegExp(`^${node}$`, 'u')
const edge = new RegExp(`^${node}\\s*(-->|---|-\\.->|==>)\\s*${node}$`, 'u')
/** Validate before Mermaid sees agent output. This intentionally supports only
 * the server's bounded flowchart-v1 grammar, never general Mermaid/HTML. */
export function validateBoardDiagram(source: string): string {
  if (new TextEncoder().encode(source).length > 32768) throw new Error('Diagram source exceeds 32 KiB.')
  const lines = source.replace(/\r\n/g, '\n').trim().split('\n').map(line => line.trim())
  if (!/^flowchart (TD|TB|BT|LR|RL)$/.test(lines.shift() ?? '')) throw new Error('Use the supported plain flowchart profile.')
  const labels = new Map<string, string | undefined>()
  let edges = 0
  const register = (id: string, label?: string) => {
    if (label !== undefined && labels.get(id) !== undefined && labels.get(id) !== label) throw new Error('A node has conflicting labels.')
    if (!labels.has(id) || label !== undefined) labels.set(id, label)
  }
  for (const line of lines) {
    if (!line) continue
    if (new TextEncoder().encode(line).length > 2048) throw new Error('Diagram line exceeds the supported limit.')
    const connection = edge.exec(line)
    if (connection) { register(connection[1], connection[2]); register(connection[4], connection[5]); edges++ }
    else { const single = declaration.exec(line); if (!single) throw new Error('Only plain nodes and one edge per line are supported. HTML, links, directives and styling are unavailable.'); register(single[1], single[2]) }
    if (labels.size > 100 || edges > 100) throw new Error('Diagrams support up to 100 nodes and 100 edges.')
  }
  if (!labels.size) throw new Error('Diagram has no nodes.')
  return [source.replace(/\r\n/g, '\n').trim().split('\n')[0].trim(), ...lines].join('\n')
}
