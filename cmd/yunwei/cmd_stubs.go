package main

import (
	"fmt"
	"io"
)

// The commands below validate their flags and configuration, then report that
// the implementation is not available yet. They are replaced as features land.

func notImplemented(name string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "命令 %q 尚未实现 (not implemented yet)\n", name)
	return 1
}
