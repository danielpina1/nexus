//go:build linux

package node

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type sampler struct {
	mu               sync.Mutex
	prevIdle, prevTo uint64
}

// cpuPercent returns CPU usage since the previous call (first call: since boot).
func (s *sampler) cpuPercent() float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0
	}
	var total, idle uint64
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dt, di := total-s.prevTo, idle-s.prevIdle
	s.prevTo, s.prevIdle = total, idle
	if dt == 0 {
		return 0
	}
	return 100 * float64(dt-di) / float64(dt)
}

func memoryPercent() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	var total, avail float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fs := strings.Fields(v)
		if len(fs) == 0 {
			continue
		}
		n, _ := strconv.ParseFloat(fs[0], 64)
		switch k {
		case "MemTotal":
			total = n
		case "MemAvailable":
			avail = n
		}
	}
	if total == 0 {
		return 0
	}
	return 100 * (total - avail) / total
}

func diskPercent(path string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0
	}
	return 100 * float64(st.Blocks-st.Bavail) / float64(st.Blocks)
}

func batteryPercent() float64 {
	for _, p := range []string{"/sys/class/power_supply/BAT0/capacity", "/sys/class/power_supply/BAT1/capacity"} {
		if b, err := os.ReadFile(p); err == nil {
			n, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			return n
		}
	}
	return 0
}
