// Barrel re-exports — preserves the original public API of prompt-input.tsx

// Context, types, providers, and hooks
export type {
  AttachmentsContext,
  TextInputContext,
  PromptInputControllerProps,
  ReferencedSourcesContext,
  PromptInputProviderProps,
} from "./prompt-input-context";
export {
  usePromptInputController,
  useProviderAttachments,
  usePromptInputAttachments,
  usePromptInputReferencedSources,
  PromptInputProvider,
  LocalReferencedSourcesContext,
} from "./prompt-input-context";

// Root component
export type { PromptInputMessage, PromptInputProps } from "./prompt-input-root";
export { PromptInput } from "./prompt-input-root";

// Textarea
export type { PromptInputTextareaProps } from "./prompt-input-textarea";
export { PromptInputTextarea } from "./prompt-input-textarea";

// Layout (body, header, footer, tools)
export type {
  PromptInputBodyProps,
  PromptInputHeaderProps,
  PromptInputFooterProps,
  PromptInputToolsProps,
} from "./prompt-input-footer";
export {
  PromptInputBody,
  PromptInputHeader,
  PromptInputFooter,
  PromptInputTools,
} from "./prompt-input-footer";

// Menu, button, submit, and action components
export type {
  PromptInputButtonTooltip,
  PromptInputButtonProps,
  PromptInputActionMenuProps,
  PromptInputActionMenuTriggerProps,
  PromptInputActionMenuContentProps,
  PromptInputActionMenuItemProps,
  PromptInputActionAddAttachmentsProps,
  PromptInputSubmitProps,
} from "./prompt-input-menu";
export {
  PromptInputButton,
  PromptInputActionMenu,
  PromptInputActionMenuTrigger,
  PromptInputActionMenuContent,
  PromptInputActionMenuItem,
  PromptInputActionAddAttachments,
  PromptInputSubmit,
} from "./prompt-input-menu";

// Select
export type {
  PromptInputSelectProps,
  PromptInputSelectTriggerProps,
  PromptInputSelectContentProps,
  PromptInputSelectItemProps,
  PromptInputSelectValueProps,
} from "./prompt-input-select";
export {
  PromptInputSelect,
  PromptInputSelectTrigger,
  PromptInputSelectContent,
  PromptInputSelectItem,
  PromptInputSelectValue,
} from "./prompt-input-select";

// HoverCard
export type {
  PromptInputHoverCardProps,
  PromptInputHoverCardTriggerProps,
  PromptInputHoverCardContentProps,
} from "./prompt-input-hover-card";
export {
  PromptInputHoverCard,
  PromptInputHoverCardTrigger,
  PromptInputHoverCardContent,
} from "./prompt-input-hover-card";

// Tabs
export type {
  PromptInputTabsListProps,
  PromptInputTabProps,
  PromptInputTabLabelProps,
  PromptInputTabBodyProps,
  PromptInputTabItemProps,
} from "./prompt-input-tabs";
export {
  PromptInputTabsList,
  PromptInputTab,
  PromptInputTabLabel,
  PromptInputTabBody,
  PromptInputTabItem,
} from "./prompt-input-tabs";

// Command
export type {
  PromptInputCommandProps,
  PromptInputCommandInputProps,
  PromptInputCommandListProps,
  PromptInputCommandEmptyProps,
  PromptInputCommandGroupProps,
  PromptInputCommandItemProps,
  PromptInputCommandSeparatorProps,
} from "./prompt-input-command";
export {
  PromptInputCommand,
  PromptInputCommandInput,
  PromptInputCommandList,
  PromptInputCommandEmpty,
  PromptInputCommandGroup,
  PromptInputCommandItem,
  PromptInputCommandSeparator,
} from "./prompt-input-command";
