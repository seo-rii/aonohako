//go:build linux

package compile

import (
	"fmt"
	"os"
	"path/filepath"

	"aonohako/internal/util"

	"golang.org/x/sys/unix"
)

type openedArtifact struct {
	file    *os.File
	info    os.FileInfo
	cleanup func()
}

func openArtifact(root, rel string) (openedArtifact, error) {
	clean, err := util.ValidateRelativePath(rel)
	if err != nil {
		return openedArtifact{}, err
	}
	dirfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return openedArtifact{}, err
	}
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(dirfd, clean, how)
	if err != nil {
		_ = unix.Close(dirfd)
		return openedArtifact{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root, clean))
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = unix.Close(dirfd)
		return openedArtifact{}, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		_ = unix.Close(dirfd)
		return openedArtifact{}, fmt.Errorf("artifact is not a regular file: %s", rel)
	}
	return openedArtifact{
		file: file,
		info: info,
		cleanup: func() {
			_ = file.Close()
			_ = unix.Close(dirfd)
		},
	}, nil
}
