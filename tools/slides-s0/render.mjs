import { escape } from './html.mjs';
import { validate } from './model.mjs';
export const effectiveFont = font => font === 'S0 Missing Font' ? 'DejaVu Sans' : font;
export function render(deck, slide) {
  validate(deck);
  const elements = slide.elements.map(e => {
    const box = `left:${e.x}px;top:${e.y}px;width:${e.w}px;height:${e.h}px;`;
    const font = e.font ? `font-family:'${effectiveFont(e.font)}';font-size:${e.size}px;color:#${e.color};` : '';
    const attrs = `data-id="${escape(e.id)}" style="${box}${font}"`;
    if (e.type === 'text') return `<p ${attrs}>${escape(e.text)}</p>`;
    if (e.type === 'shape') return `<div data-id="${e.id}" style="${box}background:#${e.fill};${e.shape === 'ellipse' ? 'border-radius:50%;' : ''}"></div>`;
    if (e.type === 'image') return `<img ${attrs} alt="${e.id}" src="data:image/png;base64,${deck.assets.find(a => a.id === e.asset).data}">`;
    return `<table ${attrs}><tbody>${e.rows.map(row => `<tr>${row.map(cell => `<td>${escape(cell)}</td>`).join('')}</tr>`).join('')}</tbody></table>`;
  }).join('');
  return `<!doctype html><html lang="cs"><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src 'none'; base-uri 'none'; form-action 'none'"><style>html,body{margin:0;width:${deck.width}px;height:${deck.height}px;overflow:hidden;background:#${deck.theme.background}}body>*{position:absolute;box-sizing:border-box;overflow:hidden}p{margin:0;white-space:pre-wrap;line-height:1.2}table{border-collapse:collapse;table-layout:fixed}td{vertical-align:top;border:1px solid #203040;padding:0;line-height:1.2}img{object-fit:fill}</style></head><body>${elements}</body></html>`;
}
