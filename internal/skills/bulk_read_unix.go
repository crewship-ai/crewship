//go:build unix

package skills

import (
	"os"
	"syscall"
)

// Nonblocking prevents a FIFO from hanging before f.Stat can reject its type.
func openSkillNonblocking(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
