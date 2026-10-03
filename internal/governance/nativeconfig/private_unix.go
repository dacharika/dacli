//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package nativeconfig

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ReadPrivate validates the opened descriptor, never a separate stat/read pair.
func ReadPrivate(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrDenied
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrDenied
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrDenied
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(st.Uid) != int64(os.Getuid()) {
		return nil, ErrDenied
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, ErrDenied
	}
	return b, nil
}
