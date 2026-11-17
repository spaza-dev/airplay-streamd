package main

import (
	"math"
	"testing"
	"time"
)

func TestParseCPUTimes(t *testing.T) {
	data := []byte(`cpu  100 20 30 400 50 5 6 0 0 0
cpu0 1 2 3 4 5 6 7 8 9 10
`)
	times, err := parseCPUTimes(data)
	if err != nil {
		t.Fatal(err)
	}
	if times.total != 611 {
		t.Fatalf("total = %d, want 611", times.total)
	}
	if times.idle != 450 {
		t.Fatalf("idle = %d, want 450", times.idle)
	}
}

func TestParseMemInfo(t *testing.T) {
	data := []byte(`MemTotal:       16000000 kB
MemFree:         2000000 kB
MemAvailable:    6000000 kB
`)
	used, total, err := parseMemInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if total != 16000000*1024 {
		t.Fatalf("total = %d", total)
	}
	if used != 10000000*1024 {
		t.Fatalf("used = %d", used)
	}
}

func TestParseNetDevSkipsLoopback(t *testing.T) {
	data := []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 999999 1 0 0 0 0 0 0 888888 1 0 0 0 0 0 0
  eth0: 1000 2 0 0 0 0 0 0 2000 2 0 0 0 0 0 0
  eth1: 3000 2 0 0 0 0 0 0 4000 2 0 0 0 0 0 0
`)
	rx, tx, err := parseNetDev(data)
	if err != nil {
		t.Fatal(err)
	}
	if rx != 4000 || tx != 6000 {
		t.Fatalf("rx=%d tx=%d, want 4000/6000", rx, tx)
	}
}

func TestParseProcessTicksHandlesSpacesInComm(t *testing.T) {
	data := []byte(`123 (ffmpeg worker) S 1 2 3 4 5 6 7 8 9 10 150 25 0 0 20 0 1 0 100 200 300`)
	ticks, err := parseProcessTicks(data)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 175 {
		t.Fatalf("ticks = %d, want 175", ticks)
	}
}

func TestParseProcessRSS(t *testing.T) {
	data := []byte(`Name:	ffmpeg
VmPeak:	  200000 kB
VmRSS:	   64000 kB
`)
	rss, err := parseProcessRSS(data)
	if err != nil {
		t.Fatal(err)
	}
	if rss != 64000*1024 {
		t.Fatalf("rss = %d", rss)
	}
}

func TestCalculateCapacityAtCapacity(t *testing.T) {
	now := time.Now()
	cfg := defaultConfig().Capacity
	cfg.CPUTargetPercent = 80
	cfg.MemoryTargetPercent = 85
	cfg.NetworkMbps = 100
	current := &MetricPoint{
		Time:             now,
		CPUPercent:       95,
		MemoryUsedBytes:  2 * 1024 * 1024 * 1024,
		MemoryTotalBytes: 8 * 1024 * 1024 * 1024,
		DiskFreeBytes:    100 * 1024 * 1024 * 1024,
		DiskTotalBytes:   200 * 1024 * 1024 * 1024,
		NetRxBytesPerSec: 5 * 1000 * 1000,
		NetTxBytesPerSec: 5 * 1000 * 1000,
		ActiveStreams:    2,
		BacklogBytes:     1000,
	}
	history := []MetricPoint{
		{Time: now.Add(-10 * time.Second), CPUPercent: 90, BacklogBytes: 1000},
		*current,
	}
	streams := []StreamMetrics{
		{ID: "a", PID: 1, CPUPercent: 20, MemoryBytes: 500 * 1024 * 1024, OutputBytesPerSec: 16000},
		{ID: "b", PID: 2, CPUPercent: 20, MemoryBytes: 500 * 1024 * 1024, OutputBytesPerSec: 16000},
	}

	estimate := calculateCapacity(cfg, current, history, streams)
	if estimate.Confidence != "measured" {
		t.Fatalf("confidence = %q", estimate.Confidence)
	}
	if estimate.Status != "at_capacity" {
		t.Fatalf("status = %q, message=%q", estimate.Status, estimate.Message)
	}
	if estimate.Bottleneck != "CPU" {
		t.Fatalf("bottleneck = %q", estimate.Bottleneck)
	}
	if estimate.RecommendedAdditionalStreams != 0 {
		t.Fatalf("recommended = %d", estimate.RecommendedAdditionalStreams)
	}
	if len(estimate.Resources) != 4 {
		t.Fatalf("resources = %+v", estimate.Resources)
	}
}

func TestCalculateCapacityUsesAssumptionsWithoutStreams(t *testing.T) {
	cfg := defaultConfig().Capacity
	current := &MetricPoint{
		Time:             time.Now(),
		CPUPercent:       10,
		MemoryUsedBytes:  1024 * 1024 * 1024,
		MemoryTotalBytes: 8 * 1024 * 1024 * 1024,
		DiskFreeBytes:    100 * 1024 * 1024 * 1024,
		DiskTotalBytes:   200 * 1024 * 1024 * 1024,
		ActiveStreams:    0,
	}
	estimate := calculateCapacity(cfg, current, nil, nil)
	if estimate.Confidence != "assumed" {
		t.Fatalf("confidence = %q", estimate.Confidence)
	}
	if estimate.RecommendedAdditionalStreams <= 0 {
		t.Fatalf("recommended = %d", estimate.RecommendedAdditionalStreams)
	}
}

func TestCPUPercent(t *testing.T) {
	previous := cpuTimes{total: 1000, idle: 600}
	current := cpuTimes{total: 2000, idle: 1100}
	if got := cpuPercent(previous, current); math.Abs(got-50) > 0.001 {
		t.Fatalf("cpu percent = %f, want 50", got)
	}
}

func TestParseBitrateBits(t *testing.T) {
	cases := map[string]float64{
		"96k":   96000,
		"128K":  128000,
		"2m":    2000000,
		"":      0,
		"copy":  0,
		"48000": 48000,
	}
	for input, want := range cases {
		if got := parseBitrateBits(input); got != want {
			t.Fatalf("parseBitrateBits(%q) = %f, want %f", input, got, want)
		}
	}
}

func TestDiskUsageBytes(t *testing.T) {
	free, total := diskUsageBytes(t.TempDir())
	if total == 0 {
		t.Fatal("total disk bytes = 0")
	}
	if free > total {
		t.Fatalf("free %d > total %d", free, total)
	}
}
