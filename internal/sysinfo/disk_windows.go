//go:build windows

package sysinfo

import "golang.org/x/sys/windows"

func diskFree(path string) float64 {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, totalFree uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil {
		return 0
	}
	return round1(float64(free) / 1e9)
}
