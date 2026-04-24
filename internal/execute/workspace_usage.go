package execute

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var (
	errWorkspaceEntryLimitExceeded = errors.New("workspace entry limit exceeded")
	errWorkspaceDepthExceeded      = errors.New("workspace depth exceeded")
)

type workspaceUsage struct {
	bytes int64
}

func scanWorkspaceUsage(root string) (workspaceUsage, error) {
	usage := workspaceUsage{}
	entries := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if path != root {
			entries++
			if entries > maxWorkspaceEntries {
				return errWorkspaceEntryLimitExceeded
			}
			rel, err := filepath.Rel(root, path)
			if err == nil && rel != "." {
				depth := 1 + strings.Count(rel, string(os.PathSeparator))
				if depth > maxWorkspaceDepth {
					return errWorkspaceDepthExceeded
				}
			}
		}
		if info.Mode().IsRegular() {
			usage.bytes += info.Size()
		}
		return nil
	})
	return usage, err
}
