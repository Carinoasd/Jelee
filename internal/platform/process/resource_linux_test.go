package process

import (
	"os"
	"strconv"
	"strings"
)

func resourceCount() int { entries, _ := os.ReadDir("/proc/self/fd"); return len(entries) }
func processAlive(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// Only absence proves exit. An unreadable status must keep waiting
		// and eventually fail rather than silently pass the cleanup check.
		return !os.IsNotExist(err)
	}
	end := strings.LastIndexByte(string(data), ')')
	return end < 0 || len(data) < end+3 || data[end+2] != 'Z'
}
