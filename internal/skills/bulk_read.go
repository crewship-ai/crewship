package skills

import (
	"fmt"
	"io"
	"os"
)

func readSkillWithinRoot(root *os.Root, name string) ([]byte, error) {
	f, err := openSkillNonblocking(root, name)
	if err != nil {
		return nil, fmt.Errorf("SKILL.md cannot be opened within import root")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("SKILL.md stat failed")
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("SKILL.md must be a regular file")
	}
	if st.Size() > maxSkillFileBytes {
		return nil, fmt.Errorf("file exceeds %d-byte SKILL.md limit", maxSkillFileBytes)
	}
	body, err := io.ReadAll(io.LimitReader(f, maxSkillFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("SKILL.md read failed")
	}
	if int64(len(body)) > maxSkillFileBytes {
		return nil, fmt.Errorf("file exceeds %d-byte SKILL.md limit", maxSkillFileBytes)
	}
	return body, nil
}
