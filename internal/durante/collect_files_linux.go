//go:build linux

package durante

import (
	"bytes"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"sort"
	"strconv"
)

// readStreams uses directory-relative no-follow descriptors and bounds before
// parsing. A target-controlled symlink/FIFO never becomes a privileged read.
func readStreams(directory string) (map[int][]event, error) {
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("report directory unavailable")
	}
	root := os.NewFile(uintptr(fd), "durante directory")
	defer root.Close()
	names, err := root.Readdirnames(maxProcesses + 1)
	if err != nil && err != io.EOF {
		return nil, errors.New("report directory read failed")
	}
	if len(names) == 0 || len(names) > maxProcesses {
		return nil, errors.New("process file count outside bound")
	}
	sort.Strings(names)
	streams := map[int][]event{}
	totalBytes := int64(0)
	for _, name := range names {
		match := reportName.FindStringSubmatch(name)
		if match == nil {
			return nil, errors.New("unexpected report entry")
		}
		pid, err := strconv.Atoi(match[1])
		if err != nil || !validPID(pid) {
			return nil, errors.New("invalid report filename PID")
		}
		data, err := readReportAt(fd, name)
		if err != nil {
			return nil, err
		}
		totalBytes += int64(len(data))
		if totalBytes > maxTotalBytes {
			return nil, errors.New("total report size exceeded")
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			return nil, errors.New("incomplete report")
		}
		lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
		if len(lines) > maxEvents {
			return nil, errors.New("event count exceeded")
		}
		rows := make([]event, 0, len(lines))
		for _, line := range lines {
			if len(line) == 0 || len(line) >= maxLineBytes {
				return nil, errors.New("event size outside bound")
			}
			row, err := decodeEvent(line)
			if err != nil {
				return nil, err
			}
			if row.PID != pid {
				return nil, errors.New("report PID mismatch")
			}
			rows = append(rows, row)
		}
		streams[pid] = rows
	}
	return streams, nil
}

func readReportAt(root int, name string) ([]byte, error) {
	fd, err := unix.Openat(root, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("report file unavailable")
	}
	file := os.NewFile(uintptr(fd), "durante report")
	defer file.Close()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 {
		return nil, errors.New("unsafe report file")
	}
	if before.Size <= 0 || before.Size > maxFileBytes {
		return nil, errors.New("report size outside bound")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || int64(len(data)) != before.Size || unix.Fstat(fd, &after) != nil || after.Size != before.Size || after.Mtim != before.Mtim || after.Ctim != before.Ctim || after.Nlink != 1 {
		return nil, errors.New("report changed or is incomplete")
	}
	return data, nil
}
