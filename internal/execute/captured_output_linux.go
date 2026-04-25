//go:build linux

package execute

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"aonohako/internal/util"

	"golang.org/x/sys/unix"
)

type capturedOutputFile struct {
	file    *os.File
	info    os.FileInfo
	cleanup func()
}

type workspaceReadOnlyFile struct {
	file    *os.File
	info    os.FileInfo
	clean   string
	dirfd   int
	cleanup func()
}

func openWorkspaceReadOnly(ws Workspace, rel string) (workspaceReadOnlyFile, error) {
	clean, err := util.ValidateRelativePath(rel)
	if err != nil {
		return workspaceReadOnlyFile{}, err
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
			return workspaceReadOnlyFile{}, err
		}
		file := os.NewFile(uintptr(fd), filepath.Join(root, clean))
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			_ = unix.Close(dirfd)
			return workspaceReadOnlyFile{}, err
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			_ = unix.Close(dirfd)
			return workspaceReadOnlyFile{}, fmt.Errorf("output is not a regular file: %s", rel)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			_ = file.Close()
			_ = unix.Close(dirfd)
			return workspaceReadOnlyFile{}, fmt.Errorf("output must not be a hard link: %s", rel)
		}
		return workspaceReadOnlyFile{
			file:  file,
			info:  info,
			clean: clean,
			dirfd: dirfd,
			cleanup: func() {
				_ = file.Close()
				_ = unix.Close(dirfd)
			},
		}, nil
	}
	return workspaceReadOnlyFile{}, os.ErrNotExist
}

func openCapturedOutput(ws Workspace, rel string) (capturedOutputFile, error) {
	output, err := openWorkspaceReadOnly(ws, rel)
	if err != nil {
		return capturedOutputFile{}, err
	}
	return capturedOutputFile{
		file: output.file,
		info: output.info,
		cleanup: func() {
			_ = unix.Unlinkat(output.dirfd, output.clean, 0)
			output.cleanup()
		},
	}, nil
}
