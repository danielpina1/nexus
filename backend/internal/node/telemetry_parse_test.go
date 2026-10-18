package node

import (
	"math"
	"testing"
)

func TestParsePSCPU(t *testing.T) {
	out := "  0.0\n 12.5\n 50,0\n 37.5\n"
	if got := parsePSCPU(out, 4); got != 25 {
		t.Fatalf("cpu = %v, want 25", got)
	}
	if got := parsePSCPU("900\n", 4); got != 100 {
		t.Fatalf("cpu not clamped: %v", got)
	}
	if parsePSCPU("", 8) != 0 {
		t.Fatal("empty output should be 0")
	}
}

func TestParseVMStat(t *testing.T) {
	out := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10000.
Pages active:                            300000.
Pages inactive:                          290000.
Pages speculative:                         5000.
Pages wired down:                        150000.
Pages purgeable:                          20000.
"Translation faults":                 123456789.
Anonymous pages:                         320000.
Pages occupied by compressor:            100000.
`
	total := uint64(1048576) * 16384 // 16 GiB
	want := 100 * float64((320000-20000)+150000+100000) / 1048576
	if got := parseVMStat(out, total); math.Abs(got-want) > 1e-9 {
		t.Fatalf("memory = %v, want %v", got, want)
	}
	if parseVMStat(out, 0) != 0 {
		t.Fatal("unknown total should be 0")
	}
}

func TestParsePmsetBattery(t *testing.T) {
	laptop := "Now drawing from 'AC Power'\n -InternalBattery-0 (id=4653155)\t87%; charged; 0:00 remaining present: true\n"
	if got := parsePmsetBattery(laptop); got != 87 {
		t.Fatalf("battery = %v", got)
	}
	if got := parsePmsetBattery("Now drawing from 'AC Power'\n"); got != 0 {
		t.Fatalf("desktop battery = %v", got)
	}
}
