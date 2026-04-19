//go:build linux

package execute

import (
	"fmt"
	"os"
	"path/filepath"

	"aonohako/internal/util"

	"golang.org/x/sys/unix"
)

type capturedOutputFile struct {
	file    *os.File
	info    os.FileInfo
	cleanup func()
}

func openCapturedOutput(ws Workspace, rel string) (capturedOutputFile, error) {
	clean, err := util.ValidateRelativePath(rel)
	if err != nil {
		return capturedOutputFile{}, err
	}
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	for _, root := range []string{ws.BoxDir, ws.RootDir} {
		dirfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		fd, err := unix.Openat2(dirfd, clean, how)
		if err != nil {
			_ = unix.Close(dirfd)
			if err == unix.ENOENT || err == unix.ENOTDIR {
				continue
			}
			return capturedOutputFile{}, err
		}
		file := os.NewFile(uintptr(fd), filepath.Join(root, clean))
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			_ = unix.Close(dirfd)
			return capturedOutputFile{}, err
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			_ = unix.Close(dirfd)
			return capturedOutputFile{}, fmt.Errorf("output is not a regular file: %s", rel)
		}
		return capturedOutputFile{
			file: file,
			info: info,
			cleanup: func() {
				_ = file.Close()
				_ = unix.Unlinkat(dirfd, clean, 0)
				_ = unix.Close(dirfd)
			},
		}, nil
	}
	return capturedOutputFile{}, os.ErrNotExist
}
