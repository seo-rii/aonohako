//go:build !linux

package processhardening

func DisableDumpability() error {
	return nil
}
