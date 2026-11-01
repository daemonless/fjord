//go:build !freebsd && !linux

package podman

import "errors"

func freeBytes(string) (uint64, error) { return 0, errors.New("free space: not supported here") }
