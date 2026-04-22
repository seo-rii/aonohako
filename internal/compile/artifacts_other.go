//go:build !linux

package compile

import (
	"fmt"
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
