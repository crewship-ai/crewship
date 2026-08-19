"use client";

import { useCallback, useRef, useState, type ComponentProps, type DragEvent } from "react";
import {
  AlertTriangle,
  File as FileIcon,
  FileImage,
  FileText,
  Paperclip,
  X,
  Upload,
} from "lucide-react";
import { AnimatePresence, motion } from "motion/react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { spring } from "@/lib/motion";

export type Attachment = {
  id: string;
  name: string;
  size: number;
  type: string;
  url?: string;
  status?: "uploading" | "ready" | "error";
  /** Why the upload was refused. Shown on the chip as its tooltip; the chip
   *  states the failure in words regardless, because a tooltip is not
   *  reachable on a phone and not announced by a screen reader. */
  error?: string;
};

export type AttachmentsProps = ComponentProps<"div"> & {
  attachments: Attachment[];
  onRemove?: (id: string) => void;
  /** Re-run a failed upload. Only offered for `status: "error"` chips, and
   *  only when the caller can actually do it (it needs the source File). */
  onRetry?: (id: string) => void;
};

export const Attachments = ({
  attachments,
  onRemove,
  onRetry,
  className,
  ...props
}: AttachmentsProps) => (
  <div
    className={cn("flex flex-wrap items-center gap-1.5", className)}
    {...props}
  >
    <AnimatePresence initial={false}>
      {attachments.map((a) => (
        <AttachmentChip
          key={a.id}
          attachment={a}
          onRemove={onRemove}
          onRetry={onRetry}
        />
      ))}
    </AnimatePresence>
  </div>
);

export type AttachmentChipProps = {
  attachment: Attachment;
  onRemove?: (id: string) => void;
  onRetry?: (id: string) => void;
};

/**
 * One file, as the composer shows it back to the user.
 *
 * The three states are deliberately told apart in WORDS, not only in colour:
 * a failed upload used to differ from a finished one by a faint destructive
 * tint and nothing else — same shape, same filename, same size — so two files
 * that had both been refused by the server read as two attached documents, and
 * the only statement that anything had gone wrong was a toast that had already
 * expired. A chip that lies about a file being attached is worse than no chip:
 * the message goes out, the agent is asked about a document, and the answer
 * comes back about a file it could not find.
 *
 * `data-status` is on the element on purpose — it is what a test asks instead
 * of matching Tailwind classes, and it is the same fact the label states.
 */
export const AttachmentChip = ({ attachment, onRemove, onRetry }: AttachmentChipProps) => {
  const failed = attachment.status === "error";
  const uploading = attachment.status === "uploading";
  const Icon = failed
    ? AlertTriangle
    : attachment.type.startsWith("image/")
      ? FileImage
      : attachment.type.startsWith("text/") || attachment.type === "application/json"
        ? FileText
        : FileIcon;

  return (
    <motion.span
      layout
      initial={{ opacity: 0, scale: 0.85, x: -8 }}
      animate={{ opacity: 1, scale: 1, x: 0 }}
      exit={{ opacity: 0, scale: 0.85, x: -8 }}
      transition={spring.snappy}
      data-status={attachment.status ?? "ready"}
      title={failed ? attachment.error : undefined}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border bg-muted/40 pl-2 pr-1 py-0.5 text-xs",
        uploading && "opacity-70",
        failed && "border-destructive bg-destructive/10 text-destructive",
      )}
    >
      <Icon
        className={cn("h-3 w-3", failed ? "text-destructive" : "text-muted-foreground")}
      />
      <span className="max-w-32 truncate">{attachment.name}</span>
      {failed ? (
        // Not a colour, not an icon: the sentence. This is the one thing on
        // screen that stops a refused upload from reading as an attachment.
        <span className="text-[10px] font-medium">Upload failed — not attached</span>
      ) : uploading ? (
        <span className="text-[10px] text-muted-foreground">Uploading…</span>
      ) : (
        <span className="text-[10px] text-muted-foreground">
          {formatBytes(attachment.size)}
        </span>
      )}
      {failed && onRetry && (
        <button
          type="button"
          onClick={() => onRetry(attachment.id)}
          className="ml-0.5 rounded-full px-1.5 py-0.5 text-[10px] font-medium underline underline-offset-2 hover:bg-destructive/15"
          aria-label={`Retry ${attachment.name}`}
        >
          Retry
        </button>
      )}
      {onRemove && (
        <button
          type="button"
          onClick={() => onRemove(attachment.id)}
          className="ml-0.5 rounded-full p-0.5 hover:bg-muted"
          aria-label={`Remove ${attachment.name}`}
        >
          <X className="h-3 w-3" />
        </button>
      )}
    </motion.span>
  );
};

export type AttachmentDropZoneProps = ComponentProps<"div"> & {
  onFiles: (files: File[]) => void;
  accept?: string;
  multiple?: boolean;
};

export const AttachmentDropZone = ({
  onFiles,
  accept,
  multiple = true,
  className,
  children,
  ...props
}: AttachmentDropZoneProps) => {
  const [over, setOver] = useState(false);
  const inputRef = useRef<HTMLInputElement | null>(null);

  const handleDrop = useCallback(
    (e: DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      setOver(false);
      const files = Array.from(e.dataTransfer.files ?? []);
      if (files.length) onFiles(files);
    },
    [onFiles],
  );

  return (
    <div
      onDragOver={(e) => {
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={handleDrop}
      className={cn("relative", className)}
      {...props}
    >
      {children}
      <AnimatePresence>
        {over && (
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.12 }}
            className="absolute inset-0 z-10 flex items-center justify-center rounded-lg border-2 border-dashed border-primary/60 bg-primary/5 backdrop-blur-sm"
          >
            <div className="flex items-center gap-2 text-sm text-primary">
              <Upload className="h-4 w-4" />
              Drop to attach
            </div>
          </motion.div>
        )}
      </AnimatePresence>
      <input
        ref={inputRef}
        type="file"
        accept={accept}
        multiple={multiple}
        className="hidden"
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          if (files.length) onFiles(files);
          e.target.value = "";
        }}
      />
    </div>
  );
};

export type AttachmentTriggerProps = Omit<ComponentProps<typeof Button>, "onSelect"> & {
  onSelect: (files: File[]) => void;
  accept?: string;
  multiple?: boolean;
};

export const AttachmentTrigger = ({
  onSelect,
  accept,
  multiple = true,
  variant = "ghost",
  size = "icon-sm",
  className,
  children,
  ...props
}: AttachmentTriggerProps) => {
  const inputRef = useRef<HTMLInputElement | null>(null);
  return (
    <>
      <input
        ref={inputRef}
        type="file"
        accept={accept}
        multiple={multiple}
        className="hidden"
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          if (files.length) onSelect(files);
          e.target.value = "";
        }}
      />
      <Button
        type="button"
        variant={variant}
        size={size}
        className={cn("h-7 w-7", className)}
        onClick={() => inputRef.current?.click()}
        {...props}
      >
        {children ?? <Paperclip className="h-3.5 w-3.5" />}
        <span className="sr-only">Attach files</span>
      </Button>
    </>
  );
};

function formatBytes(n: number): string {
  if (n < 1024) return `${n}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)}kB`;
  return `${(n / (1024 * 1024)).toFixed(1)}MB`;
}
