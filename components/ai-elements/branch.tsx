"use client";

import {
  MessageBranch,
  MessageBranchContent,
  MessageBranchSelector,
  MessageBranchPrevious,
  MessageBranchNext,
  MessageBranchPage,
} from "@/components/ai-elements/message";

export const Branch = MessageBranch;
export const BranchMessages = MessageBranchContent;
export const BranchSelector = MessageBranchSelector;
export const BranchPrevious = MessageBranchPrevious;
export const BranchNext = MessageBranchNext;
export const BranchPage = MessageBranchPage;

export type {
  MessageBranchProps as BranchProps,
  MessageBranchContentProps as BranchMessagesProps,
  MessageBranchSelectorProps as BranchSelectorProps,
  MessageBranchPreviousProps as BranchPreviousProps,
  MessageBranchNextProps as BranchNextProps,
  MessageBranchPageProps as BranchPageProps,
} from "@/components/ai-elements/message";
