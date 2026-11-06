//go:build !freebsd && !linux

package doctor

import "errors"

func freeBytes(string) (uint64, error) {
	return 0, errors.New("statfs not supported on this platform")
}
