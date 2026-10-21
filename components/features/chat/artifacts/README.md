# Saved Document panel

Saved Document cards open the current saved content beside the conversation.
The panel offers **Open full editor** after both the current Page metadata and
saved body have been read successfully. The link uses the immutable Page ID and
workspace, so a rename does not redirect it to another document. It opens in a
new tab so the conversation and its draft stay available. The editor checks
current access independently; opening it does not grant permission to edit.

`SavedDocumentPanel` composes the existing authorized resolver, controlled
viewer and strict read-only Document renderer. Its caller supplies the current
human, workspace, chat, activation epoch and verified result target. The host
keeps ownership of placement, close/expand state and opener focus.

The panel shows **Current saved document**, including for a creation card.
Historical revision previews require a separate immutable revision resolver.
During identity changes or refresh, the panel hides the prior title, body and
editor action. Denied reads and temporary service failures remain distinct.
