package rest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// An incremental depth-first scan keeps at most 16 directories open and handles
// at most 2048 entries per sampler tick. Symlinks are never followed. Scrapes
// only see the last completed pass, whose timestamp exposes eventual freshness.
type mediaScanner struct {
	stack []*os.File
	bytes int64
}

func (s *mediaScanner) close() {
	for _, f := range s.stack {
		_ = f.Close()
	}
	s.stack = nil
	s.bytes = 0
}

func (s *mediaScanner) step(ctx context.Context, root string) (total int64, complete bool, err error) {
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	if len(s.stack) == 0 {
		if root == "" {
			return 0, true, nil
		}
		fd, e := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if os.IsNotExist(e) {
			return 0, true, nil
		}
		if e != nil {
			return 0, false, e
		}
		s.stack = []*os.File{os.NewFile(uintptr(fd), root)}
		s.bytes = 0
	}
	for n := 0; n < 2048; n++ {
		if ctx.Err() != nil {
			return 0, false, nil
		}
		f := s.stack[len(s.stack)-1]
		entries, e := f.ReadDir(1)
		if e != nil && e != io.EOF {
			return 0, false, e
		}
		if len(entries) == 0 {
			_ = f.Close()
			s.stack = s.stack[:len(s.stack)-1]
			if len(s.stack) == 0 {
				total = s.bytes
				s.bytes = 0
				return total, true, nil
			}
			continue
		}
		entry := entries[0]
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if len(s.stack) == 16 {
				return 0, false, fmt.Errorf("media directory nesting exceeds metric scan limit")
			}
			fd, e := unix.Openat(int(f.Fd()), entry.Name(), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
			if errors.Is(e, unix.ENOENT) || errors.Is(e, unix.ENOTDIR) || errors.Is(e, unix.ELOOP) {
				continue
			}
			if e != nil {
				return 0, false, e
			}
			child := os.NewFile(uintptr(fd), filepath.Join(f.Name(), entry.Name()))
			s.stack = append(s.stack, child)
			continue
		}
		info, e := entry.Info()
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return 0, false, e
		}
		if info.Mode().IsRegular() {
			s.bytes += info.Size()
		}
	}
	return 0, false, nil
}
