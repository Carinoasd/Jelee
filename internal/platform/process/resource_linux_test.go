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
		return false
	}
	end := strings.LastIndexByte(string(data), ')')
	return end < 0 || len(data) < end+3 || data[end+2] != 'Z'
}
