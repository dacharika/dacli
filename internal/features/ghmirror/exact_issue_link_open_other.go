//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package ghmirror

import (
	"fmt"
	"os"
)

func openExactIssuePrivate(string) (*os.File, error) {
	return nil, fmt.Errorf("bounded nonblocking private issue-link file observation unsupported on this platform")
}
