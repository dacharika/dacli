//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package ghmirror

import (
	"os"

	"golang.org/x/sys/unix"
)

// openExactIssuePrivate uses nonblocking, no-follow flags before checking file
// type so a replaced private plan cannot make the reader wait on a FIFO.
func openExactIssuePrivate(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
