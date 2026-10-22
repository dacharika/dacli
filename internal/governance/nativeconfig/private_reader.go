package nativeconfig

import (
	"io"
	"math"
)

// readPrivateBytes also bounds a descriptor that grows after its size check.
// The extra byte distinguishes an exact-limit payload from an oversized one.
func readPrivateBytes(reader io.Reader, limit int64) ([]byte, error) {
	if limit < 0 || limit == math.MaxInt64 {
		return nil, ErrDenied
	}
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrDenied
	}
	return raw, nil
}
