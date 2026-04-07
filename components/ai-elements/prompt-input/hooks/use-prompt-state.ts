"use client";

import { useCallback, useMemo, useState } from "react";

import type { PromptInputControllerProps } from "../prompt-input-context";
import type { AttachmentsContext } from "../prompt-input-context";

import type { RefObject } from "react";

// ============================================================================
// Provider-level prompt state hook
// ============================================================================

export function usePromptState(
  initialTextInput: string,
  attachments: AttachmentsContext,
  __registerFileInput: (
    ref: RefObject<HTMLInputElement | null>,
    open: () => void
  ) => void
) {
  const [textInput, setTextInput] = useState(initialTextInput);
  const clearInput = useCallback(() => setTextInput(""), []);

  const controller = useMemo<PromptInputControllerProps>(
    () => ({
      __registerFileInput,
      attachments,
      textInput: {
        clear: clearInput,
        setInput: setTextInput,
        value: textInput,
      },
    }),
    [textInput, clearInput, attachments, __registerFileInput]
  );

  return controller;
}
