import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { useState } from "react"
import { parseRecurrence, RoutineRecurrenceFields } from "../routine-recurrence-fields"
describe("human recurrence choices", () => {
  it.each([['0 9 * * 0,6', 'weekends'], ['30 8 * * 1,3,5','weekly'], ['0 9 * * 1-5','weekdays'], ['0 9 31 * *','monthly']])("recognizes %s", (cron, frequency) => expect(parseRecurrence(cron)?.frequency).toBe(frequency))
  it.each(['0 9 * * 1-3','0 9 * 6 *','0 9 * * * extra','0 25 * * *','0 9 0 * *'])("keeps unsupported schedules intact: %s", cron => expect(parseRecurrence(cron)).toBeNull())
  it("selects weekends and several weekdays without requiring cron", () => {
    const changed = vi.fn()
    function Harness() { const [cron, setCron] = useState('0 9 * * *'); return <RoutineRecurrenceFields cron={cron} timezone="Europe/Prague" onTimezoneChange={vi.fn()} onCronChange={value => { changed(value); setCron(value) }} /> }
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Weekends' }))
    expect(changed).toHaveBeenLastCalledWith('0 9 * * 0,6')
    fireEvent.click(screen.getByRole('button', { name: 'Every week' }))
    fireEvent.click(screen.getByRole('button', { name: 'Wednesday' }))
    expect(changed).toHaveBeenLastCalledWith('0 9 * * 1,3')
    fireEvent.click(screen.getByRole('button', { name: 'Monday' }))
    expect(changed).toHaveBeenLastCalledWith('0 9 * * 3')
    fireEvent.click(screen.getByRole('button', { name: 'Wednesday' }))
    expect(changed).toHaveBeenLastCalledWith('0 9 * * 3')
  })
})
