"use client";

import { useState, type ComponentProps } from "react";
import { ChevronDown, Check, Sparkles } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  DropdownMenuLabel,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

export type ModelOption = {
  id: string;
  label: string;
  provider?: string;
  description?: string;
  badge?: string;
};

export type ModelSelectorProps = ComponentProps<typeof Button> & {
  models: ModelOption[];
  value?: string;
  onModelChange?: (id: string) => void;
};

export const ModelSelector = ({
  models,
  value,
  onModelChange,
  className,
  variant = "ghost",
  size = "sm",
  ...props
}: ModelSelectorProps) => {
  const [open, setOpen] = useState(false);
  const current = models.find((m) => m.id === value) ?? models[0];

  const grouped = models.reduce<Record<string, ModelOption[]>>((acc, m) => {
    const key = m.provider ?? "Models";
    (acc[key] ??= []).push(m);
    return acc;
  }, {});

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant={variant}
          size={size}
          className={cn(
            "h-7 gap-1.5 px-2 text-xs text-muted-foreground hover:text-foreground",
            className,
          )}
          {...props}
        >
          <Sparkles className="h-3 w-3" />
          <span className="font-mono">{current?.label ?? "Model"}</span>
          <ChevronDown className="h-3 w-3" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[260px]">
        {Object.entries(grouped).map(([provider, list], gi) => (
          <div key={provider}>
            {gi > 0 && <DropdownMenuSeparator />}
            <DropdownMenuLabel className="text-xs text-muted-foreground">
              {provider}
            </DropdownMenuLabel>
            {list.map((m) => (
              <DropdownMenuItem
                key={m.id}
                onSelect={() => onModelChange?.(m.id)}
                className="flex items-start gap-2"
              >
                <div className="flex flex-col gap-0.5 min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm">{m.label}</span>
                    {m.badge && (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-primary/10 text-primary font-medium">
                        {m.badge}
                      </span>
                    )}
                  </div>
                  {m.description && (
                    <span className="text-xs text-muted-foreground line-clamp-1">
                      {m.description}
                    </span>
                  )}
                </div>
                {m.id === current?.id && (
                  <Check className="h-4 w-4 text-primary shrink-0 mt-0.5" />
                )}
              </DropdownMenuItem>
            ))}
          </div>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
};
