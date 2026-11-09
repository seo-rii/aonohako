//go:build !linux

package workmeter

import "errors"

func readStreams(string) (map[int][]event, error) { return nil, errors.New("workmeter requires Linux") }
