import {createHash} from 'node:crypto'
import {expect,it} from 'vitest'
import {emptyDeckCraftNativeDocument} from './deckcraft-empty'
it('copies the exact original CLI title-slide ZIP for explicit Deck creation',()=>{
 const first=emptyDeckCraftNativeDocument(),second=emptyDeckCraftNativeDocument()
 expect(first).toHaveLength(4592)
 expect(createHash('sha256').update(first).digest('hex')).toBe('02e8ae7be8d5f7c8db9085f417453db4647013014a2f7a14ed68b55a01fadeda')
 expect(second).toEqual(first);expect(second).not.toBe(first)
 first[0]=0;expect(second[0]).toBe(80)
})
