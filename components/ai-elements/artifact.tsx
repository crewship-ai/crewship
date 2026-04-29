"use client";

import {
  createContext,
  useContext,
  useMemo,
  useState,
  type ComponentProps,
  type ReactNode,
} from "react";
import { AnimatePresence, motion, type HTMLMotionProps } from "motion/react";
import { X, FileCode, GitCompareArrows, Eye } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { spring } from "@/lib/motion";

type ArtifactView = "editor" | "preview" | "diff";

interface ArtifactCtx {
  view: ArtifactView;
  setView: (v: ArtifactView) => void;
  open: boolean;
  setOpen: (v: boolean) => void;
}

const Ctx = createContext<ArtifactCtx | null>(null);

const useArtifact = () => {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("Artifact.* must be used inside <Artifact>");
  return ctx;
};

export type ArtifactProps = HTMLMotionProps<"aside"> & {
  defaultView?: ArtifactView;
  open: boolean;
  onOpenChange?: (open: boolean) => void;
  width?: number;
};

export const Artifact = ({
  defaultView = "editor",
  open,
  onOpenChange,
  width = 480,
  className,
  children,
  ...props
}: ArtifactProps) => {
  const [view, setView] = useState<ArtifactView>(defaultView);
  const value = useMemo(
    () => ({
      view,
      setView,
      open,
      setOpen: (v: boolean) => onOpenChange?.(v),
    }),
    [view, open, onOpenChange],
  );

  return (
    <Ctx.Provider value={value}>
      <AnimatePresence>
        {open && (
          <motion.aside
            key="artifact"
            initial={{ x: width + 32, opacity: 0 }}
            animate={{ x: 0, opacity: 1 }}
            exit={{ x: width + 32, opacity: 0 }}
            transition={spring.bouncy}
            style={{ width }}
            className={cn(
              "fixed top-0 right-0 z-30 h-full bg-background border-l shadow-2xl flex flex-col",
              className,
            )}
            {...props}
          >
            {children}
          </motion.aside>
        )}
      </AnimatePresence>
    </Ctx.Provider>
  );
};

export type ArtifactHeaderProps = ComponentProps<"header"> & {
  title?: ReactNode;
  subtitle?: ReactNode;
  onClose?: () => void;
};

export const ArtifactHeader = ({
  title,
  subtitle,
  onClose,
  className,
  children,
  ...props
}: ArtifactHeaderProps) => {
  const { setOpen } = useArtifact();
  return (
    <header
      className={cn(
        "flex items-center gap-3 border-b px-4 py-3 shrink-0",
        className,
      )}
      {...props}
    >
      <div className="flex flex-col gap-0.5 min-w-0 flex-1">
        {title && (
          <span className="text-sm font-medium truncate">{title}</span>
        )}
        {subtitle && (
          <span className="text-xs text-muted-foreground truncate">
            {subtitle}
          </span>
        )}
      </div>
      {children}
      <Button
        size="icon-sm"
        variant="ghost"
        onClick={() => {
          onClose?.();
          setOpen(false);
        }}
        aria-label="Close artifact"
      >
        <X className="h-4 w-4" />
      </Button>
    </header>
  );
};

export type ArtifactViewSwitchProps = ComponentProps<"div">;

export const ArtifactViewSwitch = ({
  className,
  ...props
}: ArtifactViewSwitchProps) => {
  const { view, setView } = useArtifact();
  return (
    <div
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md border bg-muted/40 p-0.5 text-xs",
        className,
      )}
      {...props}
    >
      {(
        [
          { id: "editor", icon: FileCode, label: "Editor" },
          { id: "diff", icon: GitCompareArrows, label: "Diff" },
          { id: "preview", icon: Eye, label: "Preview" },
        ] as const
      ).map(({ id, icon: Icon, label }) => (
        <button
          key={id}
          type="button"
          onClick={() => setView(id)}
          className={cn(
            "relative inline-flex items-center gap-1 rounded px-2 py-1 transition-colors",
            view === id
              ? "text-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
          aria-pressed={view === id}
        >
          {view === id && (
            <motion.span
              layoutId="artifact-view-indicator"
              transition={spring.snappy}
              className="absolute inset-0 rounded bg-background shadow-sm border"
            />
          )}
          <Icon className="relative h-3 w-3" />
          <span className="relative">{label}</span>
        </button>
      ))}
    </div>
  );
};

export type ArtifactBodyProps = ComponentProps<"div"> & {
  editor: ReactNode;
  preview?: ReactNode;
  diff?: ReactNode;
};

export const ArtifactBody = ({
  editor,
  preview,
  diff,
  className,
  ...props
}: ArtifactBodyProps) => {
  const { view } = useArtifact();
  return (
    <div
      className={cn("flex-1 overflow-hidden relative", className)}
      {...props}
    >
      <AnimatePresence mode="wait">
        <motion.div
          key={view}
          initial={{ opacity: 0, y: 4 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: -4 }}
          transition={{ duration: 0.15 }}
          className="h-full"
        >
          {view === "editor" && editor}
          {view === "diff" && (diff ?? editor)}
          {view === "preview" && (preview ?? editor)}
        </motion.div>
      </AnimatePresence>
    </div>
  );
};
