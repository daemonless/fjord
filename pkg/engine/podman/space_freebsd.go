package podman

import "syscall"

// freeBytes is the space an unprivileged write can use on path's filesystem.
func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * st.Bsize, nil
}
