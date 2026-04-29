"use client";

import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ComponentProps,
  type ReactNode,
} from "react";
import { ChevronDown, Loader2, CheckCircle2, Circle } from "lucide-react";
import { AnimatePresence, motion, type HTMLMotionProps } from "motion/react";

import { cn } from "@/lib/utils";
import { spring, stagger } from "@/lib/motion";

type Status = "pending" | "active" | "complete";

interface ChainCtx {
  open: boolean;
  setOpen: (v: boolean) => void;
  isStreaming?: boolean;
}

const Ctx = createContext<ChainCtx | null>(null);

const useChain = () => {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("ChainOfThought.* must be used inside ChainOfThought");
  return ctx;
};

export type ChainOfThoughtProps = ComponentProps<"div"> & {
  defaultOpen?: boolean;
  isStreaming?: boolean;
};

export const ChainOfThought = ({
  defaultOpen = false,
  isStreaming,
  className,
  children,
  ...props
}: ChainOfThoughtProps) => {
  const [open, setOpen] = useState(defaultOpen || !!isStreaming);
  useEffect(() => {
    if (isStreaming) setOpen(true);
  }, [isStreaming]);
  const value = useMemo(() => ({ open, setOpen, isStreaming }), [open, isStreaming]);
  return (
    <Ctx.Provider value={value}>
      <div className={cn("flex flex-col gap-1", className)} {...props}>
        {children}
      </div>
    </Ctx.Provider>
  );
};

export type ChainOfThoughtTriggerProps = ComponentProps<"button"> & {
  label?: string;
};

export const ChainOfThoughtTrigger = ({
  label = "Reasoning",
  className,
  ...props
}: ChainOfThoughtTriggerProps) => {
  const { open, setOpen, isStreaming } = useChain();
  return (
    <button
      type="button"
      {...props}
      onClick={(e) => {
        props.onClick?.(e);
        if (e.defaultPrevented || isStreaming) return;
        setOpen(!open);
      }}
      disabled={isStreaming}
      aria-disabled={isStreaming}
      aria-busy={isStreaming}
      aria-expanded={open}
      className={cn(
        "flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors w-fit",
        className,
      )}
    >
      {isStreaming ? (
        <Loader2 className="h-3 w-3 animate-spin" />
      ) : (
        <motion.span animate={{ rotate: open ? 0 : -90 }} transition={spring.snappy}>
          <ChevronDown className="h-3 w-3" />
        </motion.span>
      )}
      <span>{label}</span>
    </button>
  );
};

export type ChainOfThoughtContentProps = HTMLMotionProps<"ol">;

export const ChainOfThoughtContent = ({
  className,
  children,
  ...props
}: ChainOfThoughtContentProps) => {
  const { open } = useChain();
  return (
    <AnimatePresence initial={false}>
      {open && (
        <motion.div
          initial={{ height: 0, opacity: 0 }}
          animate={{ height: "auto", opacity: 1 }}
          exit={{ height: 0, opacity: 0 }}
          transition={spring.smooth}
          className="overflow-hidden"
        >
          <motion.ol
            variants={{ closed: {}, open: stagger.steps }}
            initial="closed"
            animate="open"
            className={cn(
              "ml-1.5 mt-1 flex flex-col gap-1.5 border-l border-border pl-3 text-sm text-muted-foreground",
              className,
            )}
            {...props}
          >
            {children}
          </motion.ol>
        </motion.div>
      )}
    </AnimatePresence>
  );
};

export type ChainStepProps = HTMLMotionProps<"li"> & {
  status?: Status;
  title: ReactNode;
  description?: ReactNode;
};

export const ChainStep = ({
  status = "pending",
  title,
  description,
  className,
  ...props
}: ChainStepProps) => {
  const Icon =
    status === "complete" ? CheckCircle2 : status === "active" ? Loader2 : Circle;

  return (
    <motion.li
      initial={{ opacity: 0, x: -6 }}
      animate={{ opacity: 1, x: 0 }}
      transition={spring.smooth}
      className={cn("flex items-start gap-2", className)}
      {...props}
    >
      <Icon
        className={cn(
          "h-3.5 w-3.5 shrink-0 mt-0.5",
          status === "complete" && "text-emerald-500",
          status === "active" && "text-amber-500 animate-spin",
          status === "pending" && "text-muted-foreground/40",
        )}
      />
      <div className="flex flex-col gap-0.5 min-w-0">
        <span
          className={cn(
            "text-xs",
            status === "complete" && "text-foreground",
            status === "active" && "text-foreground font-medium",
          )}
        >
          {title}
        </span>
        {description && (
          <span className="text-xs text-muted-foreground/80">{description}</span>
        )}
      </div>
    </motion.li>
  );
};
