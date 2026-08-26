//go:build linux

package compile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"aonohako/internal/util"

	"golang.org/x/sys/unix"
)

type openedArtifact struct {
	file    *os.File
	info    os.FileInfo
	cleanup func()
}

func openRegularArtifactAt(dirfd int, root, clean string) (*os.File, os.FileInfo, error) {
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(dirfd, clean, how)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root, clean))
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("artifact is not a regular file: %s", clean)
	}
	return file, info, nil
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
	file, info, err := openRegularArtifactAt(dirfd, root, clean)
	if err != nil {
		_ = unix.Close(dirfd)
		return openedArtifact{}, err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		_ = file.Close()
		_ = unix.Close(dirfd)
		return openedArtifact{}, fmt.Errorf("artifact must not be a hard link: %s", rel)
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

// snapshotCargoArtifact accepts Cargo's expected hard-linked release binary,
// but never reopens that pathname after validating it. It copies from the
// validated descriptor into a private, single-link file and returns the still
// open snapshot descriptor to the response reader.
func snapshotCargoArtifact(root, rel string) (openedArtifact, error) {
	clean, err := util.ValidateRelativePath(rel)
	if err != nil {
		return openedArtifact{}, err
	}
	dirfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return openedArtifact{}, err
	}
	source, sourceInfo, err := openRegularArtifactAt(dirfd, root, clean)
	if err != nil {
		_ = unix.Close(dirfd)
		return openedArtifact{}, err
	}
	defer source.Close()
	if sourceInfo.Size() > maxArtifactBytes {
		_ = unix.Close(dirfd)
		return openedArtifact{}, fmt.Errorf("artifact too large: %s", rel)
	}

	var snapshotName string
	snapshotFD := -1
	for range 8 {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			_ = unix.Close(dirfd)
			return openedArtifact{}, err
		}
		snapshotName = ".aonohako-artifact-" + hex.EncodeToString(suffix[:])
		snapshotFD, err = unix.Openat(
			dirfd,
			snapshotName,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		if err == nil {
			break
		}
		if err != unix.EEXIST {
			_ = unix.Close(dirfd)
			return openedArtifact{}, err
		}
	}
	if snapshotFD < 0 {
		_ = unix.Close(dirfd)
		return openedArtifact{}, fmt.Errorf("could not create a private artifact snapshot")
	}

	snapshotFile := os.NewFile(uintptr(snapshotFD), filepath.Join(root, snapshotName))
	snapshot := openedArtifact{
		file: snapshotFile,
		cleanup: func() {
			_ = snapshotFile.Close()
			_ = unix.Unlinkat(dirfd, snapshotName, 0)
			_ = unix.Close(dirfd)
		},
	}
	succeeded := false
	defer func() {
		if !succeeded {
			snapshot.cleanup()
		}
	}()

	written, err := io.Copy(snapshotFile, io.LimitReader(source, maxArtifactBytes+1))
	if err != nil {
		return openedArtifact{}, err
	}
	if written > maxArtifactBytes {
		return openedArtifact{}, fmt.Errorf("artifact too large: %s", rel)
	}
	if _, err := snapshotFile.Seek(0, io.SeekStart); err != nil {
		return openedArtifact{}, err
	}
	snapshot.info, err = snapshotFile.Stat()
	if err != nil {
		return openedArtifact{}, err
	}
	stat, ok := snapshot.info.Sys().(*syscall.Stat_t)
	if !snapshot.info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		return openedArtifact{}, fmt.Errorf("artifact snapshot is not a private regular file")
	}
	succeeded = true
	return snapshot, nil
}
