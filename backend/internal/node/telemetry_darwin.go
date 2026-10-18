//go:build darwin

package node

import (
	"context"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// macOS telemetry without cgo: the stock ps, vm_stat, sysctl and pmset
// tools, each bounded by a short timeout. Values are best-effort.
type sampler struct{}

func darwinOutput(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// cpuPercent is the machine-wide CPU use: the sum of every process's %CPU
// over the number of cores.
func (s *sampler) cpuPercent() float64 {
	return parsePSCPU(darwinOutput("/bin/ps", "-A", "-o", "%cpu="), runtime.NumCPU())
}

// memoryPercent matches Activity Monitor's "Memory Used": app memory, wired
// and compressed pages over physical memory.
func memoryPercent() float64 {
	total := parseUint(darwinOutput("/usr/sbin/sysctl", "-n", "hw.memsize"))
	return parseVMStat(darwinOutput("/usr/bin/vm_stat"), total)
}

func diskPercent(path string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0
	}
	return 100 * float64(st.Blocks-st.Bavail) / float64(st.Blocks)
}

func batteryPercent() float64 {
	return parsePmsetBattery(darwinOutput("/usr/bin/pmset", "-g", "batt"))
}
