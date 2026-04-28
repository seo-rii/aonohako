package workspacequota

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxEntries = 8192
	MaxDepth   = 32
)

var (
	ErrEntryLimitExceeded = errors.New("workspace entry limit exceeded")
	ErrDepthExceeded      = errors.New("workspace depth exceeded")
)

type Usage struct {
	Bytes int64
}

func Scan(root string) (Usage, error) {
	usage := Usage{}
	entries := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if path != root {
			entries++
			if entries > MaxEntries {
				return ErrEntryLimitExceeded
			}
			rel, err := filepath.Rel(root, path)
			if err == nil && rel != "." {
				depth := 1 + strings.Count(rel, string(os.PathSeparator))
				if depth > MaxDepth {
					return ErrDepthExceeded
				}
			}
		}
		if info.Mode().IsRegular() {
			usage.Bytes += info.Size()
		}
		return nil
	})
	return usage, err
}
