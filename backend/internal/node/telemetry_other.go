//go:build !linux && !darwin

package node

// Telemetry is best-effort; unsupported platforms report zeros.
type sampler struct{}

func (s *sampler) cpuPercent() float64 { return 0 }
func memoryPercent() float64           { return 0 }
func diskPercent(string) float64       { return 0 }
func batteryPercent() float64          { return 0 }
