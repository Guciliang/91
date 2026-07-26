package drives

import (
	"errors"
	"io"
	"strings"
)

// MaxSTRMBytes bounds the small text payload read from a .strm file.
const MaxSTRMBytes = 64 * 1024

// ReadSTRMTarget returns the first non-empty line in a .strm payload. A UTF-8
// byte-order mark on the first line is ignored for compatibility with common
// .strm editors.
func ReadSTRMTarget(r io.Reader) (string, error) {
	if r == nil {
		return "", errors.New("strm reader is required")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxSTRMBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxSTRMBytes {
		return "", errors.New("strm file is too large")
	}
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if line = strings.TrimSpace(line); line != "" {
			return line, nil
		}
	}
	return "", errors.New("empty strm target")
}
