//go:build linux

package storage

import (
	"syscall"
)

type DiskSpace struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

func GetDiskSpace(path string) (DiskSpace, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return DiskSpace{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	used := (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
	return DiskSpace{
		TotalBytes: total,
		FreeBytes:  free,
		UsedBytes:  used,
	}, nil
}
