"use client";

import type { ComponentProps, ReactNode } from "react";

import { useControllableState } from "@radix-ui/react-use-controllable-state";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";
import { ChevronDownIcon } from "lucide-react";
import { BrainIcon } from "@/components/ui/brain";
import { formatDurationFloor } from "@/lib/time";
import {
  createContext,
  memo,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Streamdown } from "streamdown";

import { useStreamdownPlugins } from "./use-streamdown-plugins";

import { Shimmer } from "./shimmer";

interface ReasoningContextValue {
  isStreaming: boolean;
  isOpen: boolean;
  setIsOpen: (open: boolean) => void;
  duration: number | undefined;
  /** Whole seconds elapsed since streaming started; ticks live while open. */
  elapsed: number;
}

const ReasoningContext = createContext<ReasoningContextValue | null>(null);

export const useReasoning = () => {
  const context = useContext(ReasoningContext);
  if (!context) {
    throw new Error("Reasoning components must be used within Reasoning");
  }
  return context;
};

export type ReasoningProps = ComponentProps<typeof Collapsible> & {
  isStreaming?: boolean;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  duration?: number;
};

const AUTO_CLOSE_DELAY = 1000;
const MS_IN_S = 1000;

export const Reasoning = memo(
  ({
    className,
    isStreaming = false,
    open,
    defaultOpen,
    onOpenChange,
    duration: durationProp,
    children,
    ...props
  }: ReasoningProps) => {
    const resolvedDefaultOpen = defaultOpen ?? isStreaming;
    // Track if defaultOpen was explicitly set to false (to prevent auto-open)
    const isExplicitlyClosed = defaultOpen === false;

    const [isOpen, setIsOpen] = useControllableState<boolean>({
      defaultProp: resolvedDefaultOpen,
      onChange: onOpenChange,
      prop: open,
    });
    const [duration, setDuration] = useControllableState<number | undefined>({
      defaultProp: undefined,
      prop: durationProp,
    });

    const hasEverStreamedRef = useRef(isStreaming);
    const [hasAutoClosed, setHasAutoClosed] = useState(false);
    const startTimeRef = useRef<number | null>(null);
    // Total thinking time across episodes: a turn's merged reasoning block
    // streams in several passes (think → text → think), so the timer must
    // accumulate rather than restart from zero on each pass.
    const accumulatedMsRef = useRef(0);
    const [elapsed, setElapsed] = useState(0);

    // Track streaming episodes and accumulate the total duration.
    useEffect(() => {
      if (isStreaming) {
        hasEverStreamedRef.current = true;
        if (startTimeRef.current === null) {
          startTimeRef.current = Date.now();
        }
        // A new episode reopens the block; allow it to auto-close again when
        // the final episode ends.
        setHasAutoClosed(false);
      } else if (startTimeRef.current !== null) {
        accumulatedMsRef.current += Date.now() - startTimeRef.current;
        startTimeRef.current = null;
        setDuration(Math.ceil(accumulatedMsRef.current / MS_IN_S));
      }
    }, [isStreaming, setDuration]);

    // Live elapsed ticker while streaming — the header shows "Thinking… Ns"
    // so a long reasoning pass reads as progress, not a hang.
    useEffect(() => {
      if (!isStreaming) return;
      const tick = () => {
        if (startTimeRef.current !== null) {
          setElapsed(
            Math.floor(
              (accumulatedMsRef.current + Date.now() - startTimeRef.current) / MS_IN_S
            )
          );
        }
      };
      tick();
      const interval = setInterval(tick, MS_IN_S);
      return () => clearInterval(interval);
    }, [isStreaming]);

    // Auto-open when streaming starts (unless explicitly closed)
    useEffect(() => {
      if (isStreaming && !isOpen && !isExplicitlyClosed) {
        setIsOpen(true);
      }
    }, [isStreaming, isOpen, setIsOpen, isExplicitlyClosed]);

    // Auto-close when streaming ends (once only, and only if it ever streamed)
    useEffect(() => {
      if (
        hasEverStreamedRef.current &&
        !isStreaming &&
        isOpen &&
        !hasAutoClosed
      ) {
        const timer = setTimeout(() => {
          setIsOpen(false);
          setHasAutoClosed(true);
        }, AUTO_CLOSE_DELAY);

        return () => clearTimeout(timer);
      }
    }, [isStreaming, isOpen, setIsOpen, hasAutoClosed]);

    const handleOpenChange = useCallback(
      (newOpen: boolean) => {
        setIsOpen(newOpen);
      },
      [setIsOpen]
    );

    const contextValue = useMemo(
      () => ({ duration, isOpen, isStreaming, setIsOpen, elapsed }),
      [duration, isOpen, isStreaming, setIsOpen, elapsed]
    );

    return (
      <ReasoningContext.Provider value={contextValue}>
        <Collapsible
          className={cn("not-prose mb-4", className)}
          onOpenChange={handleOpenChange}
          open={isOpen}
          {...props}
        >
          {children}
        </Collapsible>
      </ReasoningContext.Provider>
    );
  }
);

export type ReasoningTriggerProps = ComponentProps<
  typeof CollapsibleTrigger
> & {
  getThinkingMessage?: (
    isStreaming: boolean,
    duration?: number,
    elapsed?: number
  ) => ReactNode;
};

/** Collapsed-header label once reasoning is done. Exported for tests. */
export const thoughtForLabel = (duration?: number): string => {
  if (duration === undefined) return "Thought for a few seconds";
  if (duration < 60) {
    return `Thought for ${duration} ${duration === 1 ? "second" : "seconds"}`;
  }
  return `Thought for ${formatDurationFloor(duration * MS_IN_S)}`;
};

/** Live header label while reasoning streams. Exported for tests. */
export const thinkingLiveLabel = (elapsed: number): string =>
  elapsed >= 1 ? `Thinking… ${formatDurationFloor(elapsed * MS_IN_S)}` : "Thinking…";

const defaultGetThinkingMessage = (
  isStreaming: boolean,
  duration?: number,
  elapsed?: number
) => {
  if (isStreaming || duration === 0) {
    return <Shimmer duration={1.6}>{thinkingLiveLabel(elapsed ?? 0)}</Shimmer>;
  }
  return <p>{thoughtForLabel(duration)}</p>;
};

export const ReasoningTrigger = memo(
  ({
    className,
    children,
    getThinkingMessage = defaultGetThinkingMessage,
    ...props
  }: ReasoningTriggerProps) => {
    const { isStreaming, isOpen, duration, elapsed } = useReasoning();

    return (
      <CollapsibleTrigger
        className={cn(
          "flex w-full items-center gap-2 text-muted-foreground text-sm transition-colors hover:text-foreground",
          className
        )}
        {...props}
      >
        {children ?? (
          <>
            <BrainIcon size={16} />
            {/* aria-live off: the label ticks every second while streaming —
                assistive tech must not announce each update. */}
            <span aria-live="off">
              {getThinkingMessage(isStreaming, duration, elapsed)}
            </span>
            <ChevronDownIcon
              className={cn(
                "size-4 transition-transform",
                isOpen ? "rotate-180" : "rotate-0"
              )}
            />
          </>
        )}
      </CollapsibleTrigger>
    );
  }
);

export type ReasoningContentProps = ComponentProps<
  typeof CollapsibleContent
> & {
  children: string;
};

export const ReasoningContent = memo(
  ({ className, children, ...props }: ReasoningContentProps) => {
    // Mermaid loads lazily, only for content that carries a mermaid fence.
    const plugins = useStreamdownPlugins(children);
    return (
      <CollapsibleContent
        className={cn(
          "mt-4 text-sm",
          "data-[state=closed]:fade-out-0 data-[state=closed]:slide-out-to-top-2 data-[state=open]:slide-in-from-top-2 text-muted-foreground outline-none data-[state=closed]:animate-out data-[state=open]:animate-in",
          className
        )}
        {...props}
      >
        <Streamdown plugins={plugins}>
          {children}
        </Streamdown>
      </CollapsibleContent>
    );
  }
);

Reasoning.displayName = "Reasoning";
ReasoningTrigger.displayName = "ReasoningTrigger";
ReasoningContent.displayName = "ReasoningContent";
