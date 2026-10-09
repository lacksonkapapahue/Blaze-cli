//go:build !windows

package sysinfo

import "syscall"

func diskFree(path string) float64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0
	}
	return round1(float64(s.Bavail) * float64(s.Bsize) / 1e9)
}
