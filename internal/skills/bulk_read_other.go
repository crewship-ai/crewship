//go:build !unix

package skills

import "os"

func openSkillNonblocking(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
