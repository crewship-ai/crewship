"use client"

import { useState } from "react"
import { Bot, Check, ChevronsUpDown, User, UserX } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { cn } from "@/lib/utils"

export interface AssigneeOption {
  id: string
  name: string
  type: "user" | "agent"
  slug?: string
}

interface AssigneePickerProps {
  value: { type: string | null; id: string | null }
  onChange: (type: "user" | "agent" | null, id: string | null) => void
  agents: AssigneeOption[]
  users: AssigneeOption[]
  className?: string
}

export function AssigneePicker({
  value,
  onChange,
  agents,
  users,
  className,
}: AssigneePickerProps) {
  const [open, setOpen] = useState(false)

  const selectedName = (() => {
    if (!value.id || !value.type) return "Unassigned"
    const all = [...agents, ...users]
    const found = all.find((o) => o.id === value.id && o.type === value.type)
    return found?.name ?? "Unassigned"
  })()

  const isSelected = (option: AssigneeOption) =>
    value.id === option.id && value.type === option.type

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className={cn(
            "h-8 justify-between text-xs font-normal",
            className,
          )}
        >
          <span className="flex items-center gap-1.5 truncate">
            {value.id && value.type === "agent" && (
              <Bot className="h-3 w-3 shrink-0 text-muted-foreground" />
            )}
            {value.id && value.type === "user" && (
              <User className="h-3 w-3 shrink-0 text-muted-foreground" />
            )}
            {!value.id && (
              <UserX className="h-3 w-3 shrink-0 text-muted-foreground" />
            )}
            {selectedName}
          </span>
          <ChevronsUpDown className="ml-1 h-3 w-3 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[220px] p-0" align="start">
        <Command>
          <CommandInput placeholder="Search assignee..." className="h-8 text-xs" />
          <CommandList>
            <CommandEmpty>No results found.</CommandEmpty>
            <CommandGroup>
              <CommandItem
                onSelect={() => {
                  onChange(null, null)
                  setOpen(false)
                }}
              >
                <UserX className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                <span className="text-xs">Unassigned</span>
                {!value.id && (
                  <Check className="ml-auto h-3.5 w-3.5" />
                )}
              </CommandItem>
            </CommandGroup>
            {agents.length > 0 && (
              <CommandGroup heading="Agents">
                {agents.map((agent) => (
                  <CommandItem
                    key={agent.id}
                    onSelect={() => {
                      onChange("agent", agent.id)
                      setOpen(false)
                    }}
                  >
                    <Bot className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                    <span className="text-xs">{agent.name}</span>
                    {isSelected(agent) && (
                      <Check className="ml-auto h-3.5 w-3.5" />
                    )}
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
            {users.length > 0 && (
              <CommandGroup heading="Users">
                {users.map((user) => (
                  <CommandItem
                    key={user.id}
                    onSelect={() => {
                      onChange("user", user.id)
                      setOpen(false)
                    }}
                  >
                    <User className="mr-2 h-3.5 w-3.5 text-muted-foreground" />
                    <span className="text-xs">{user.name}</span>
                    {isSelected(user) && (
                      <Check className="ml-auto h-3.5 w-3.5" />
                    )}
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
