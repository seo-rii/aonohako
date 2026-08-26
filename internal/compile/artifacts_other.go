//go:build !linux

package compile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/util"
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
	current := root
	parts := strings.Split(clean, string(filepath.Separator))
	var info os.FileInfo
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil {
			return openedArtifact{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return openedArtifact{}, fmt.Errorf("artifact path contains a symlink: %s", rel)
		}
	}
	if !info.Mode().IsRegular() {
		return openedArtifact{}, fmt.Errorf("artifact is not a regular file: %s", rel)
	}
	file, err := os.Open(current)
	if err != nil {
		return openedArtifact{}, err
	}
	return openedArtifact{
		file: file,
		info: info,
		cleanup: func() {
			_ = file.Close()
		},
	}, nil
}

func snapshotCargoArtifact(root, rel string) (openedArtifact, error) {
	source, err := openArtifact(root, rel)
	if err != nil {
		return openedArtifact{}, err
	}
	defer source.cleanup()
	if source.info.Size() > maxArtifactBytes {
		return openedArtifact{}, fmt.Errorf("artifact too large: %s", rel)
	}

	snapshotFile, err := os.CreateTemp(root, ".aonohako-artifact-*")
	if err != nil {
		return openedArtifact{}, err
	}
	snapshot := openedArtifact{
		file: snapshotFile,
		cleanup: func() {
			_ = snapshotFile.Close()
			_ = os.Remove(snapshotFile.Name())
		},
	}
	succeeded := false
	defer func() {
		if !succeeded {
			snapshot.cleanup()
		}
	}()

	written, err := io.Copy(snapshotFile, io.LimitReader(source.file, maxArtifactBytes+1))
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
	succeeded = true
	return snapshot, nil
}
