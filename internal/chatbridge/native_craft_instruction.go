package chatbridge

import "errors"

// NativeCraftInstructionMetadataKey marks a send that only records the human's
// instruction for an explicit native Craft task. No agent turn follows, so the
// private document instruction reaches no ordinary run, chat log or agent
// diagnostics. The task itself runs under strict content privacy.
const NativeCraftInstructionMetadataKey = "native_craft_instruction"

var errNativeCraftInstruction = errors.New("native Craft instruction is invalid")

// nativeCraftInstruction reads the client's request. It can only withhold a
// run, never grant one, so client metadata is sufficient; anything but a
// literal true is refused rather than treated as an ordinary send.
func nativeCraftInstruction(metadata map[string]any) (bool, error) {
	raw, exists := metadata[NativeCraftInstructionMetadataKey]
	if !exists {
		return false, nil
	}
	if raw != true {
		return false, errNativeCraftInstruction
	}
	return true, nil
}
