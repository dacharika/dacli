//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package store

import (
	"fmt"
	"os"
)

func checkpointOpen(string) (*os.File, error) {
	return nil, fmt.Errorf("bounded checkpoint filesystem observation unsupported on this platform")
}
func checkpointChild(*os.File, string) (*os.File, error) {
	return nil, fmt.Errorf("bounded checkpoint filesystem observation unsupported on this platform")
}
