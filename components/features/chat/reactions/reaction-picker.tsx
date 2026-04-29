"use client"

import { useState } from "react"
import { Smile } from "lucide-react"
import {
  EmojiPicker,
  type EmojiPickerListEmojiProps,
  type EmojiPickerListRowProps,
  type Emoji,
} from "frimousse"

import { Action } from "@/components/ai-elements/actions"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"

interface ReactionPickerProps {
  onPick: (emoji: string) => void
  align?: "start" | "center" | "end"
}

const Row = ({ children, ...props }: EmojiPickerListRowProps) => (
  <div className="flex" {...props}>
    {children}
  </div>
)

const EmojiButton = ({ emoji, ...props }: EmojiPickerListEmojiProps) => (
  <button
    type="button"
    className="h-7 w-7 inline-flex items-center justify-center rounded text-base hover:bg-accent data-[active=true]:bg-accent"
    data-active={emoji.isActive}
    {...props}
  >
    {emoji.emoji}
  </button>
)

export function ReactionPicker({ onPick, align = "start" }: ReactionPickerProps) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Action tooltip="React">
          <Smile className="h-3.5 w-3.5" />
        </Action>
      </PopoverTrigger>
      <PopoverContent
        align={align}
        className="p-1 w-[280px] h-[320px] overflow-hidden"
      >
        <EmojiPicker.Root
          onEmojiSelect={(e: Emoji) => {
            onPick(e.emoji)
            setOpen(false)
          }}
          className="h-full flex flex-col"
        >
          <EmojiPicker.Search
            placeholder="Search emoji…"
            className="w-full rounded border bg-muted/30 px-2 py-1 text-xs mb-1 outline-none focus:ring-1 focus:ring-primary"
          />
          <EmojiPicker.Viewport className="flex-1 overflow-y-auto">
            <EmojiPicker.Loading className="text-xs text-muted-foreground p-2 block">
              Loading…
            </EmojiPicker.Loading>
            <EmojiPicker.Empty className="text-xs text-muted-foreground p-2 block">
              No emoji
            </EmojiPicker.Empty>
            <EmojiPicker.List components={{ Row, Emoji: EmojiButton }} />
          </EmojiPicker.Viewport>
        </EmojiPicker.Root>
      </PopoverContent>
    </Popover>
  )
}
