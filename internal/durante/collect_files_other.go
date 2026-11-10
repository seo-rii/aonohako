//go:build !linux

package durante

import "errors"

func readStreams(string) (map[int][]event, error) { return nil, errors.New("durante requires Linux") }
