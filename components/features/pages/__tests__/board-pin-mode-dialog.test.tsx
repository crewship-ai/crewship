import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { BoardPinModeDialog, pinModeIntent } from '../board-pin-mode-dialog'
import type { BoardPlacement } from '@/hooks/use-page-board'
afterEach(cleanup)
const pin: BoardPlacement = { id: 'pin', placement_rev: 8, mode: 'fixed', x: 0, y: 0, width: 100, height: 100, source: { page_slug: 'report', title: 'Report', revision: 3, pinned_revision: 3 } }
it('captures exact pin authority and makes following latest an explicit confirmation', () => {
  const intent = pinModeIntent(4, pin)
  expect(intent.batch).toMatchObject({ base_seq: 4, placement_modes: [{ id: 'pin', expected_rev: 8, mode: 'follow_latest', source_revision: 3 }] })
  const confirm = vi.fn(); render(<BoardPinModeDialog intent={intent} pending={false} uncertain={false} error={null} onClose={vi.fn()} onConfirm={confirm} />)
  expect(confirm).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Follow latest' }))
  expect(confirm).toHaveBeenCalledOnce()
})
it('freezes the displayed revision when turning following off and preserves unknown-operation retry', () => {
  const intent = pinModeIntent(4, { ...pin, mode: 'follow_latest', source: { ...pin.source!, revision: 7, pinned_revision: 1 } })
  expect(intent.batch.placement_modes?.[0]).toMatchObject({ mode: 'fixed', source_revision: 7 })
  render(<BoardPinModeDialog intent={intent} pending={false} uncertain error="Response lost" onClose={vi.fn()} onConfirm={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Retry mode change' })).toBeEnabled()
})
