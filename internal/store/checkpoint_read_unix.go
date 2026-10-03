//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

// Open nonblocking before checking the descriptor: opening a FIFO must not wait
// for a writer. Children are anchored to an already-open directory descriptor,
// so replacement of a parent path cannot redirect the observation.
func checkpointOpen(path string) (*os.File, error) {
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), path), nil
}
func checkpointChild(parent *os.File, name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("invalid checkpoint child")
	}
	fd, e := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), name), nil
}
