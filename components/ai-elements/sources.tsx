"use client";

import { type ComponentProps, type ReactNode } from "react";
import { ExternalLink, BookOpen, ChevronDown } from "lucide-react";
import { AnimatePresence, motion, type HTMLMotionProps } from "motion/react";

import { cn } from "@/lib/utils";
import { spring } from "@/lib/motion";

export type SourcesProps = ComponentProps<"div"> & {
  count?: number;
};

export const Sources = ({ className, children, ...props }: SourcesProps) => (
  <div className={cn("flex flex-col gap-2", className)} {...props}>
    {children}
  </div>
);

export type SourcesTriggerProps = ComponentProps<"button"> & {
  count: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

export const SourcesTrigger = ({
  count,
  open,
  onOpenChange,
  className,
  onClick,
  ...props
}: SourcesTriggerProps) => {
  return (
    <button
      type="button"
      onClick={(e) => {
        onOpenChange(!open);
        onClick?.(e);
      }}
      aria-expanded={open}
      className={cn(
        "flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors w-fit",
        className,
      )}
      data-state={open ? "open" : "closed"}
      {...props}
    >
      <BookOpen className="h-3 w-3" />
      <span>
        {count} source{count !== 1 ? "s" : ""}
      </span>
      <motion.span animate={{ rotate: open ? 0 : -90 }} transition={spring.snappy}>
        <ChevronDown className="h-3 w-3" />
      </motion.span>
    </button>
  );
};

export type SourcesContentProps = HTMLMotionProps<"ul"> & {
  open?: boolean;
};

export const SourcesContent = ({
  open = true,
  className,
  children,
  ...props
}: SourcesContentProps) => (
  <AnimatePresence initial={false}>
    {open && (
      <motion.ul
        initial={{ height: 0, opacity: 0 }}
        animate={{ height: "auto", opacity: 1 }}
        exit={{ height: 0, opacity: 0 }}
        transition={spring.smooth}
        className={cn("flex flex-col gap-1 overflow-hidden", className)}
        {...props}
      >
        {children}
      </motion.ul>
    )}
  </AnimatePresence>
);

export type SourceProps = ComponentProps<"a"> & {
  index: number;
  title: ReactNode;
  url?: string;
  snippet?: string;
};

export const Source = ({
  index,
  title,
  url,
  snippet,
  className,
  ...props
}: SourceProps) => (
  <li>
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        "flex items-start gap-2 rounded-md border bg-muted/30 px-3 py-2 text-xs hover:bg-muted/60 transition-colors",
        className,
      )}
      {...props}
    >
      <span className="rounded bg-background border px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground shrink-0">
        {index}
      </span>
      <div className="flex flex-col gap-0.5 min-w-0">
        <span className="font-medium text-foreground truncate">{title}</span>
        {snippet && <span className="text-muted-foreground line-clamp-2">{snippet}</span>}
      </div>
      {url && <ExternalLink className="h-3 w-3 ml-auto shrink-0 mt-0.5 text-muted-foreground" />}
    </a>
  </li>
);

export type InlineCitationProps = ComponentProps<"a"> & {
  index: number;
};

export const InlineCitation = ({
  index,
  className,
  href,
  ...props
}: InlineCitationProps) => (
  <a
    href={href}
    target="_blank"
    rel="noopener noreferrer"
    className={cn(
      "inline-flex items-center justify-center min-w-4 h-4 px-1 rounded bg-primary/10 text-primary text-[10px] font-medium hover:bg-primary/20 transition-colors align-super",
      className,
    )}
    {...props}
  >
    {index}
  </a>
);
