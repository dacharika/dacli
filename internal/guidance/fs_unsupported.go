//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package guidance

import (
	"errors"
	"fmt"
	"os"
)

type guidanceDir struct{ path string }

var errGuidanceFilesystemUnsupported = errors.New("safe guidance filesystem operations are unsupported on this platform")

func openGuidanceDir(string) (*guidanceDir, error)   { return nil, errGuidanceFilesystemUnsupported }
func (*guidanceDir) Close() error                    { return nil }
func (*guidanceDir) Stat() (os.FileInfo, error)      { return nil, errGuidanceFilesystemUnsupported }
func (*guidanceDir) Entries() ([]os.DirEntry, error) { return nil, errGuidanceFilesystemUnsupported }
func (*guidanceDir) OpenDir(string) (*guidanceDir, error) {
	return nil, errGuidanceFilesystemUnsupported
}
func (*guidanceDir) ReadRegular(string, int64) ([]byte, os.FileInfo, error) {
	return nil, nil, errGuidanceFilesystemUnsupported
}
func (*guidanceDir) CreateExclusive(string, os.FileMode) (*os.File, error) {
	return nil, errGuidanceFilesystemUnsupported
}
func (*guidanceDir) Mkdir(string, os.FileMode) error { return errGuidanceFilesystemUnsupported }
func (*guidanceDir) Rename(string, *guidanceDir, string) error {
	return errGuidanceFilesystemUnsupported
}
func (*guidanceDir) Remove(string) error { return errGuidanceFilesystemUnsupported }
func (*guidanceDir) SamePath() bool      { return false }
func guidanceFileIdentity(info os.FileInfo) string {
	return fmt.Sprintf("mode:%s;size:%d;mod:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
}
func guidanceSingleLink(os.FileInfo) bool { return false }
func guidanceFilesystemUnsupported(err error) bool {
	return errors.Is(err, errGuidanceFilesystemUnsupported)
}
