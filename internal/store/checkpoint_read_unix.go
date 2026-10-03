//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Open nonblocking before checking the descriptor: opening a FIFO must not wait
// for a writer. Children are anchored to an already-open directory descriptor,
// so replacement of a parent path cannot redirect the observation.
func checkpointOpen(path string) (*os.File, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), path), nil
}
func checkpointChild(parent *os.File, name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("invalid checkpoint child")
	}
	return checkpointOpen(filepath.Join("/dev/fd", fmt.Sprint(parent.Fd()), name))
}
