//go:build !linux

package execute

import (
	"os"
	"path/filepath"
	"strings"

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

func existingWorkspacePath(ws Workspace, rel string) (string, error) {
	for _, candidate := range workspacePathCandidates(ws, rel) {
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func workspacePathCandidates(ws Workspace, rel string) []string {
	if strings.HasPrefix(filepath.ToSlash(rel), "__img__/") {
		return []string{
			filepath.Join(ws.RootDir, rel),
			filepath.Join(ws.BoxDir, rel),
		}
	}
	return []string{filepath.Join(ws.BoxDir, rel)}
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
