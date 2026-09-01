import { describe, it, expect } from 'vitest'

/**
 * Guards the `Animation.prototype.cancel` patch in vitest.setup.ts.
 *
 * happy-dom builds `Animation.finished` eagerly and rejects it on cancel, so
 * every animation motion tears down on unmount produced an unhandled
 * rejection. Without the patch this file fails on the first case below.
 */
describe('happy-dom animation cancellation', () => {
  const collectUnhandled = async (run: () => void) => {
    const seen: unknown[] = []
    const onUnhandled = (reason: unknown) => seen.push(reason)
    process.on('unhandledRejection', onUnhandled)
    try {
      run()
      // Node reports an unhandled rejection once the microtask queue has
      // drained, so one macrotask turn is enough to observe it.
      await new Promise((resolve) => setTimeout(resolve, 0))
    } finally {
      process.off('unhandledRejection', onUnhandled)
    }
    return seen
  }

  it('does not report an unhandled rejection when nobody awaited finished', async () => {
    const seen = await collectUnhandled(() => {
      const element = document.createElement('div')
      const animation = element.animate([{ opacity: 0 }, { opacity: 1 }], {
        duration: 50,
      })
      animation.cancel()
    })

    expect(seen).toEqual([])
  })

  it('still rejects finished for a caller that awaited it', async () => {
    const element = document.createElement('div')
    const animation = element.animate([{ opacity: 0 }, { opacity: 1 }], {
      duration: 50,
    })

    const finished = animation.finished
    animation.cancel()

    await expect(finished).rejects.toThrow(/canceled/i)
  })
})
