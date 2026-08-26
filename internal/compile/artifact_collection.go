package compile

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

func passThroughArtifacts(workDir string, sources []model.Source) model.CompileResponse {
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
}

func readSingleArtifact(root, rel, name, mode string) ([]model.Artifact, error) {
	artifact, err := openArtifact(root, rel)
	if err != nil {
		return nil, fmt.Errorf("read artifact failed: %w", err)
	}
	return readOpenedArtifact(artifact, name, mode)
}

func readOpenedArtifact(artifact openedArtifact, name, mode string) ([]model.Artifact, error) {
	defer artifact.cleanup()
	if artifact.info.Size() > maxArtifactBytes {
		return nil, fmt.Errorf("artifact too large: %s", name)
	}
	data, err := io.ReadAll(artifact.file)
	if err != nil {
		return nil, fmt.Errorf("read artifact failed: %w", err)
	}
	return []model.Artifact{{Name: name, DataB64: util.EncodeB64(data), Mode: mode}}, nil
}

func collectArtifacts(root string, include func(name string) bool, prefix string) ([]model.Artifact, error) {
	var artifacts []model.Artifact
	var totalBytes int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("artifact path contains a symlink: %s", d.Name())
		}
		if include != nil && !include(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		artifact, err := openArtifact(root, rel)
		if err != nil {
			return err
		}
		info := artifact.info
		if info.Size() > maxArtifactBytes {
			artifact.cleanup()
			return fmt.Errorf("artifact too large: %s", d.Name())
		}
		totalBytes += info.Size()
		if totalBytes > maxArtifactTotalBytes {
			artifact.cleanup()
			return fmt.Errorf("artifact total size exceeded")
		}
		data, err := io.ReadAll(artifact.file)
		if err != nil {
			artifact.cleanup()
			return err
		}
		name := filepath.ToSlash(rel)
		if prefix != "" {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		mode := ""
		if info.Mode().Perm()&0o111 != 0 {
			mode = "exec"
		}
		artifacts = append(artifacts, model.Artifact{Name: name, DataB64: util.EncodeB64(data), Mode: mode})
		artifact.cleanup()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return artifacts, nil
}
