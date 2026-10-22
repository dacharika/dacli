//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package nativeconfig

import (
	"os"
	"path/filepath"
	"syscall"
)

// ReadMetadata reads one explicitly selected public metadata file through a
// no-follow descriptor and a strict byte bound. Unlike ReadPrivate it does not
// require owner-only permissions because package manifests are public.
func ReadMetadata(path string, limit int64) ([]byte, error) {
	f, err := openMetadata(path, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readPrivateBytes(f, limit)
}

// InspectExecutableMetadata confirms one explicitly selected artifact is a
// bounded regular executable file. It does not execute or parse the artifact.
func InspectExecutableMetadata(path string, limit int64) error {
	f, err := openMetadata(path, limit)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		return ErrDenied
	}
	return nil
}

func openMetadata(path string, limit int64) (*os.File, error) {
	if !filepath.IsAbs(path) || limit <= 0 {
		return nil, ErrDenied
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrDenied
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		_ = f.Close()
		return nil, ErrDenied
	}
	return f, nil
}
