//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package guidance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

type guidanceDir struct {
	file *os.File
	path string
}

func openGuidanceDir(path string) (*guidanceDir, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("directory path must be absolute and clean")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	cur := os.NewFile(uintptr(fd), string(filepath.Separator))
	rel := strings.TrimPrefix(path, string(filepath.Separator))
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		nextFD, e := unix.Openat(int(cur.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			_ = cur.Close()
			return nil, e
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(cur.Name(), part))
		_ = cur.Close()
		cur = next
	}
	return &guidanceDir{file: cur, path: path}, nil
}

func (d *guidanceDir) Close() error                    { return d.file.Close() }
func (d *guidanceDir) Stat() (os.FileInfo, error)      { return d.file.Stat() }
func (d *guidanceDir) Entries() ([]os.DirEntry, error) { return d.file.ReadDir(-1) }

func (d *guidanceDir) OpenDir(rel string) (*guidanceDir, error) {
	if rel == "" || rel == "." {
		fd, err := unix.Dup(int(d.file.Fd()))
		if err != nil {
			return nil, err
		}
		return &guidanceDir{file: os.NewFile(uintptr(fd), d.path), path: d.path}, nil
	}
	parts, err := cleanGuidanceParts(rel)
	if err != nil {
		return nil, err
	}
	curFD, err := unix.Dup(int(d.file.Fd()))
	if err != nil {
		return nil, err
	}
	cur := os.NewFile(uintptr(curFD), d.path)
	for _, part := range parts {
		nextFD, e := unix.Openat(int(cur.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			_ = cur.Close()
			return nil, e
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(cur.Name(), part))
		_ = cur.Close()
		cur = next
	}
	return &guidanceDir{file: cur, path: filepath.Join(d.path, filepath.FromSlash(rel))}, nil
}

func (d *guidanceDir) ReadRegular(name string, limit int64) ([]byte, os.FileInfo, error) {
	if !validGuidanceName(name) {
		return nil, nil, errors.New("invalid file name")
	}
	fd, err := unix.Openat(int(d.file.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.path, name))
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() < 0 || opened.Size() > limit {
		return nil, nil, fmt.Errorf("%s is not a bounded regular file", f.Name())
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, nil, fmt.Errorf("%s exceeds read limit", f.Name())
	}
	final, err := f.Stat()
	if err != nil || !os.SameFile(opened, final) || final.Size() != int64(len(b)) {
		return nil, nil, fmt.Errorf("%s changed while reading", f.Name())
	}
	return b, opened, nil
}

func (d *guidanceDir) CreateExclusive(name string, mode os.FileMode) (*os.File, error) {
	if !validGuidanceName(name) {
		return nil, errors.New("invalid file name")
	}
	fd, err := unix.Openat(int(d.file.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(d.path, name)), nil
}

func (d *guidanceDir) Mkdir(name string, mode os.FileMode) error {
	if !validGuidanceName(name) {
		return errors.New("invalid directory name")
	}
	return unix.Mkdirat(int(d.file.Fd()), name, uint32(mode.Perm()))
}

func (d *guidanceDir) Rename(oldName string, to *guidanceDir, newName string) error {
	if !validGuidanceName(oldName) || !validGuidanceName(newName) {
		return errors.New("invalid rename name")
	}
	return unix.Renameat(int(d.file.Fd()), oldName, int(to.file.Fd()), newName)
}

func (d *guidanceDir) Remove(name string) error {
	if !validGuidanceName(name) {
		return errors.New("invalid file name")
	}
	return unix.Unlinkat(int(d.file.Fd()), name, 0)
}

func (d *guidanceDir) SamePath() bool {
	current, err := openGuidanceDir(d.path)
	if err != nil {
		return false
	}
	defer current.Close()
	a, err := d.file.Stat()
	if err != nil {
		return false
	}
	b, err := current.file.Stat()
	return err == nil && os.SameFile(a, b)
}

func guidanceFileIdentity(info os.FileInfo) string {
	v := reflect.ValueOf(info.Sys())
	if v.IsValid() && v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.IsValid() && v.Kind() == reflect.Struct {
		dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
		if dev.IsValid() && ino.IsValid() && dev.CanInterface() && ino.CanInterface() {
			return fmt.Sprintf("dev:%v;ino:%v;mode:%o", dev.Interface(), ino.Interface(), info.Mode())
		}
	}
	return fmt.Sprintf("mode:%s;size:%d;mod:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
}

func guidanceSingleLink(info os.FileInfo) bool {
	v := reflect.ValueOf(info.Sys())
	if v.IsValid() && v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return false
	}
	nlink := v.FieldByName("Nlink")
	if !nlink.IsValid() || !nlink.CanInterface() {
		return false
	}
	return fmt.Sprint(nlink.Interface()) == "1"
}

func guidanceFilesystemUnsupported(error) bool { return false }
