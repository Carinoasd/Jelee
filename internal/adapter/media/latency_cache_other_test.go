//go:build perf && !linux

package media

// dropPageCache is unavailable off Linux; cold-cache rows are skipped.
func dropPageCache(string) bool { return false }

func residentPercent(string) float64 { return -1 }

func filesystemName(string) string { return "unknown" }
