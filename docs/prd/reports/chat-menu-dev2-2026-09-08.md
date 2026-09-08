# New chat menu polish — Dev2, 2026-09-08

Small visual change only: neutral compact outline trigger with chevron; direct
messages and shared conversations grouped separately, with action labels and
short descriptions. Existing agent/person/group/channel handlers preserved.
No sidebar restructure or backend change.

Nine unified-chat tests, targeted ESLint, Go vet and the standard Dev2 reload
(static build/TypeScript included) passed. The unchanged Go backend had already
passed the complete 133-package suite in the preceding Files iteration.
Normal Emma live login verified all menu options and the person/group/channel
dialogs without creating any conversations. Screenshot: assets/chat-new-menu-dev2.png.
