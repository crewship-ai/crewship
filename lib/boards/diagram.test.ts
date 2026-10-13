import { expect, it } from 'vitest'
import { validateBoardDiagram } from './diagram'
it('accepts bounded Unicode plain flowcharts and normalizes lines', () => {
  expect(validateBoardDiagram(' flowchart TD\r\n A[Plán] --> B[Build]\r\n B -.-> C[Review] ')).toBe('flowchart TD\nA[Plán] --> B[Build]\nB -.-> C[Review]')
})
it.each(['flowchart TD\nA[<img src=x>]','flowchart TD\nclick A "https://evil.test"','flowchart TD\nA[https://evil.test]','%%{init:{}}%%\nflowchart TD\nA','flowchart TD\nsubgraph evil','flowchart TD\nA --> B --> C','flowchart TD\nA[One]\nA[Two]'])('rejects unsupported source before invoking Mermaid: %s', source => { expect(() => validateBoardDiagram(source)).toThrow() })
it('bounds nodes, edges and bytes', () => { expect(() => validateBoardDiagram('flowchart TD\n'+Array.from({length:101},(_,i)=>`A${i}`).join('\n'))).toThrow('100 nodes'); expect(() => validateBoardDiagram('x'.repeat(32769))).toThrow('32 KiB') })
