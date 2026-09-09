"use client"

import { useState } from "react"
import { CalendarDays } from "lucide-react"
import { enGB } from "date-fns/locale"
import { Calendar } from "@/components/ui/calendar"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"

export function RoutineDateTimePicker({ value, onChange, label = "Choose date" }: { value: string; onChange: (value: string) => void; label?: string }) {
  const [open, setOpen] = useState(false)
  const parsed = value ? new Date(value) : undefined
  const date = parsed && !Number.isNaN(parsed.getTime()) ? parsed : undefined
  const time = value.split("T")[1]?.slice(0, 5) || "09:00"
  const today = new Date(); today.setHours(0, 0, 0, 0)
  const select = (day: Date | undefined) => {
    if (!day) return
    onChange(`${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, "0")}-${String(day.getDate()).padStart(2, "0")}T${time}`)
    setOpen(false)
  }
  return <div className="flex flex-wrap items-center gap-3"><Popover open={open} onOpenChange={setOpen} modal><PopoverTrigger asChild><button type="button" aria-label={label} className="inline-flex h-10 items-center gap-2 rounded-xl border border-border/60 bg-muted/30 px-3 text-sm"><CalendarDays className="h-4 w-4 text-muted-foreground" />{date ? date.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short", year: "numeric" }) : "Choose date"}</button></PopoverTrigger><PopoverContent align="start" className="w-auto p-0"><Calendar locale={enGB} mode="single" selected={date} defaultMonth={date} onSelect={select} disabled={{ before: today }} /></PopoverContent></Popover><Input aria-label="Start time" type="time" className="w-32" value={time} disabled={!date} onChange={e => { if (e.target.value && value) onChange(`${value.split("T")[0]}T${e.target.value}`) }} /></div>
}
