import { test, expect } from '@playwright/test'
import { startBoardFixture } from './fixture'

test('production Board keeps the built-in Mermaid converter outside Studio', async ({ page }) => {
 const fixture = await startBoardFixture({ product: true })
 const converterLoads: string[] = []
 page.on('request', request => { if (/mermaid-to-excalidraw/.test(request.url())) converterLoads.push(request.url()) })
 try {
  await page.route('**/api/v1/**', route => {
   const path=new URL(route.request().url()).pathname
   if(path.endsWith('/board')) return route.fulfill({json:{format:'crewship-board-excalidraw-0.18.1-v1',seq:0,can_write:true,records:[],placements:[]}})
   if(path.endsWith('/board/files')) return route.fulfill({json:{files:[]}})
   if(path.endsWith('/board/threads')) return route.fulfill({json:{threads:[],can_comment:true,can_resolve:true}})
   return route.fulfill({json:{requests:[],eligible_chats:[],permissions:[],eligible_runs:[]}})
  })
  await page.goto(new URL('product.html',fixture.url).href)
  await page.getByRole('button',{name:'More tools',exact:true}).click()
  await expect(page.getByRole('button',{name:/Mermaid to Excalidraw/i})).toBeHidden()
  // Even a synthetic click of the upstream item cannot mount its converter.
  await page.locator('[data-testid="toolbar-embeddable"]').filter({ hasText: 'Mermaid to Excalidraw' }).dispatchEvent('click')
  await expect(page.locator('.ttd-dialog')).toHaveCount(0)
  expect(converterLoads).toEqual([])
 } finally { await page.close(); await fixture.close() }
})
