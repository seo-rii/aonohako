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

type workspaceReadOnlyFile struct {
	file    *os.File
	info    os.FileInfo
	full    string
	cleanup func()
}

func openWorkspaceReadOnly(ws Workspace, rel string) (workspaceReadOnlyFile, error) {
	clean, err := util.ValidateRelativePath(rel)
	if err != nil {
		return workspaceReadOnlyFile{}, err
	}
	full, err := existingWorkspacePath(ws, clean)
	if err != nil {
		return workspaceReadOnlyFile{}, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return workspaceReadOnlyFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return workspaceReadOnlyFile{}, os.ErrPermission
	}
	if !info.Mode().IsRegular() {
		return workspaceReadOnlyFile{}, os.ErrInvalid
	}
	file, err := os.Open(full)
	if err != nil {
		return workspaceReadOnlyFile{}, err
	}
	return workspaceReadOnlyFile{
		file: file,
		info: info,
		full: full,
		cleanup: func() {
			_ = file.Close()
		},
	}, nil
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
			_ = os.Remove(output.full)
			output.cleanup()
		},
	}, nil
}
