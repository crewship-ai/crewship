import { describe, it, expect } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { AnimatePresence, motion } from 'motion/react'

/**
 * Guards `supportsFlags.linearEasing = false` in vitest.setup.ts.
 *
 * motion 14.7+ runs transform values through a grouped WAAPI animation that
 * happy-dom never finishes, so an exit that animates x left the element in
 * the DOM forever (#4488, #4763). Without the flag the exit cases time out.
 */
describe('motion transform animations under happy-dom', () => {
  const panel = (open: boolean, transition: object, onEntered = () => {}) => (
    <AnimatePresence>
      {open && (
        <motion.aside
          aria-label="Panel"
          initial={{ x: 360, opacity: 0 }}
          animate={{ x: 0, opacity: 1 }}
          exit={{ x: 360, opacity: 0 }}
          transition={transition}
          onAnimationComplete={onEntered}
        />
      )}
    </AnimatePresence>
  )

  it.each([
    ['spring', { type: 'spring', damping: 28, stiffness: 320 }],
    ['tween', { duration: 0.05 }],
  ])('removes an element whose %s exit moves x', async (_name, transition) => {
    let entered = false
    const { rerender } = render(panel(true, transition, () => (entered = true)))
    expect(screen.getByRole('complementary', { name: 'Panel' })).toBeInTheDocument()
    // Close only once the panel is in place, as a user would.
    await waitFor(() => expect(entered).toBe(true))
    rerender(panel(false, transition))
    await waitFor(() => expect(screen.queryByRole('complementary')).not.toBeInTheDocument())
  })

  it('completes an entry animation on x', async () => {
    let completed = false
    render(
      <motion.div
        initial={{ x: 10 }}
        animate={{ x: 0 }}
        transition={{ duration: 0.05 }}
        onAnimationComplete={() => {
          completed = true
        }}
      />,
    )
    await waitFor(() => expect(completed).toBe(true))
  })
})
