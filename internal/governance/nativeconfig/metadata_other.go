//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package nativeconfig

// Public metadata reads require a descriptor-safe platform implementation.
func ReadMetadata(string, int64) ([]byte, error) { return nil, ErrDenied }

func InspectExecutableMetadata(string, int64) error { return ErrDenied }
