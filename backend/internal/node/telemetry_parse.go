package node

import (
	"regexp"
	"strconv"
	"strings"
)

// Parsers for the macOS telemetry tools (telemetry_darwin.go). They live in
// a file without build tags so they are tested on every platform.

// parsePSCPU sums `ps -A -o %cpu=` and divides by the core count.
func parsePSCPU(out string, cores int) float64 {
	if out == "" || cores <= 0 {
		return 0
	}
	var sum float64
	for _, f := range strings.Fields(out) {
		if v, err := strconv.ParseFloat(strings.ReplaceAll(f, ",", "."), 64); err == nil {
			sum += v
		}
	}
	return clampPercent(sum / float64(cores))
}

var vmStatLine = regexp.MustCompile(`^(.+?):\s+(\d+)\.?$`)

// parseVMStat turns `vm_stat` output into used memory percent:
// (anonymous app pages - purgeable + wired + compressor) * page size / total.
func parseVMStat(out string, totalBytes uint64) float64 {
	if out == "" || totalBytes == 0 {
		return 0
	}
	pageSize := uint64(4096)
	if m := regexp.MustCompile(`page size of (\d+) bytes`).FindStringSubmatch(out); m != nil {
		pageSize, _ = strconv.ParseUint(m[1], 10, 64)
	}
	pages := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		if m := vmStatLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, _ := strconv.ParseUint(m[2], 10, 64)
			pages[strings.Trim(m[1], `"`)] = n
		}
	}
	app := pages["Anonymous pages"]
	if p := pages["Pages purgeable"]; app > p {
		app -= p
	}
	if app == 0 { // older vm_stat without anonymous pages
		app = pages["Pages active"]
	}
	used := (app + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * pageSize
	return clampPercent(100 * float64(used) / float64(totalBytes))
}

var pmsetPercent = regexp.MustCompile(`(\d+)%`)

// parsePmsetBattery reads the charge from `pmset -g batt`; 0 without a battery.
func parsePmsetBattery(out string) float64 {
	if !strings.Contains(out, "InternalBattery") {
		return 0
	}
	if m := pmsetPercent.FindStringSubmatch(out); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v
	}
	return 0
}

func parseUint(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

func clampPercent(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}
