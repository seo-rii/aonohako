//go:build !linux

package execute

import (
	"os"

	"aonohako/internal/util"
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
	full, err := existingWorkspacePath(ws, clean)
	if err != nil {
		return capturedOutputFile{}, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return capturedOutputFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return capturedOutputFile{}, os.ErrPermission
	}
	if !info.Mode().IsRegular() {
		return capturedOutputFile{}, os.ErrInvalid
	}
	file, err := os.Open(full)
	if err != nil {
		return capturedOutputFile{}, err
	}
	return capturedOutputFile{
		file: file,
		info: info,
		cleanup: func() {
			_ = file.Close()
			_ = os.Remove(full)
		},
	}, nil
}
