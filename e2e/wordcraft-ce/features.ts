export interface Feature {
  id: string; group: string; command: string; parameters?: Record<string, unknown>
  control?: { name: string; value: string }
  character?: { field: string; value: unknown }
  paragraph?: { field: string; value: unknown }
  key?: string
}
export const features: Feature[] = [
  { id: 'text', group: 'text-enter', command: 'text.insert', parameters: { text: 'Z' }, key: 'Z' },
  { id: 'enter', group: 'text-enter', command: 'text.newParagraph', parameters: {}, key: 'Enter' },
  { id: 'font', group: 'character-format', command: 'format.font', parameters: { name: 'Arial' }, control: { name: 'Font', value: 'Arial' }, character: { field: 'font', value: 'Arial' } },
  { id: 'size', group: 'character-format', command: 'format.size', parameters: { size: 18 }, control: { name: 'Font Size', value: '18' }, character: { field: 'size', value: 18 } },
  { id: 'color', group: 'character-format', command: 'format.color', parameters: { color: '123456' }, control: { name: 'Font Color', value: '123456' }, character: { field: 'color', value: { Rgb: [18, 52, 86] } } },
  { id: 'highlight', group: 'character-format', command: 'format.highlight', parameters: { color: 'yellow' }, control: { name: 'Text Highlight Color', value: 'yellow' }, character: { field: 'highlight', value: 'yellow' } },
  { id: 'bold', group: 'character-format', command: 'format.bold', key: 'Control+b', character: { field: 'bold', value: true } },
  { id: 'italic', group: 'character-format', command: 'format.italic', key: 'Control+i', character: { field: 'italic', value: true } },
  { id: 'paragraphs', group: 'paragraphs', command: 'para.align', parameters: { value: 'center' }, control: { name: 'Alignment', value: 'center' }, paragraph: { field: 'align', value: 'center' } },
  { id: 'bullets', group: 'lists', command: 'para.bullets' },
  { id: 'numbering', group: 'lists', command: 'para.numbering' },
]
