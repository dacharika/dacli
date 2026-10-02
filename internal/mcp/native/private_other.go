//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package native

// Private credential semantics require a verified platform adapter.
func ReadPrivate(string, int64) ([]byte, error) { return nil, ErrDenied }
