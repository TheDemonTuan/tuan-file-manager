//go:build !linux && !windows

package storage

type DiskSpace struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

func GetDiskSpace(path string) (DiskSpace, error) {
	return DiskSpace{TotalBytes: 200 * 1024 * 1024 * 1024, FreeBytes: 150 * 1024 * 1024 * 1024}, nil
}
