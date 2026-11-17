package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	metricsUnlimitedStreams = 9999
	procClockTicksPerSecond = 100
	defaultStreamBitrate    = 128000.0
)

type MetricPoint struct {
	Time             time.Time `json:"time"`
	CPUPercent       float64   `json:"cpu_percent"`
	MemoryUsedBytes  uint64    `json:"memory_used_bytes"`
	MemoryTotalBytes uint64    `json:"memory_total_bytes"`
	DiskFreeBytes    uint64    `json:"disk_free_bytes"`
	DiskTotalBytes   uint64    `json:"disk_total_bytes"`
	NetRxBytesPerSec float64   `json:"net_rx_bytes_per_sec"`
	NetTxBytesPerSec float64   `json:"net_tx_bytes_per_sec"`
	ActiveStreams    int       `json:"active_streams"`
	BacklogBytes     int64     `json:"backlog_bytes"`
	Load1            float64   `json:"load_1"`
}

type StreamMetrics struct {
	ID                string     `json:"id"`
	PID               int        `json:"pid,omitempty"`
	CPUPercent        float64    `json:"cpu_percent"`
	MemoryBytes       uint64     `json:"memory_bytes"`
	OutputBytesPerSec float64    `json:"output_bytes_per_sec"`
	ChunksPerMinute   float64    `json:"chunks_per_minute"`
	LastChunkAt       *time.Time `json:"last_chunk_at,omitempty"`
	BytesTotal        int64      `json:"bytes_total"`
	ChunksTotal       int64      `json:"chunks_total"`
}

type ResourceHeadroom struct {
	Name              string  `json:"name"`
	Unit              string  `json:"unit"`
	Used              float64 `json:"used"`
	Limit             float64 `json:"limit"`
	UsedPercent       float64 `json:"used_percent"`
	AdditionalStreams int     `json:"additional_streams"`
	Limiting          bool    `json:"limiting"`
}

type CapacityEstimate struct {
	Status                       string             `json:"status"`
	Bottleneck                   string             `json:"bottleneck,omitempty"`
	RecommendedAdditionalStreams int                `json:"recommended_additional_streams"`
	Confidence                   string             `json:"confidence"`
	ActiveStreams                int                `json:"active_streams"`
	Resources                    []ResourceHeadroom `json:"resources"`
	Message                      string             `json:"message,omitempty"`
}

type MetricsSnapshot struct {
	Enabled         bool             `json:"enabled"`
	IntervalSeconds int              `json:"interval_seconds"`
	Current         *MetricPoint     `json:"current,omitempty"`
	History         []MetricPoint    `json:"history"`
	Streams         []StreamMetrics  `json:"streams"`
	Capacity        CapacityEstimate `json:"capacity"`
}

type CapacityExceededError struct {
	StreamID string
	Estimate CapacityEstimate
}

func (e *CapacityExceededError) Error() string {
	bottleneck := e.Estimate.Bottleneck
	if bottleneck == "" {
		bottleneck = "system resources"
	}
	return fmt.Sprintf("server at capacity: %s is the limiting resource; about %d additional stream(s) supported (use force to override)",
		bottleneck, e.Estimate.RecommendedAdditionalStreams)
}

type cpuTimes struct {
	total uint64
	idle  uint64
}

type netTimes struct {
	rx uint64
	tx uint64
}

type streamSample struct {
	pid    int
	ticks  uint64
	bytes  int64
	chunks int64
}

type MetricsCollector struct {
	app *App

	mu          sync.RWMutex
	current     *MetricPoint
	history     []MetricPoint
	streams     []StreamMetrics
	maxHistory  int
	hasPrev     bool
	prevTime    time.Time
	prevCPU     cpuTimes
	prevNet     netTimes
	prevStreams map[string]streamSample

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newMetricsCollector(app *App) *MetricsCollector {
	cfg := app.Config().Metrics
	maxHistory := 2
	if cfg.SampleIntervalSeconds > 0 {
		maxHistory = cfg.HistorySeconds / cfg.SampleIntervalSeconds
		if maxHistory < 2 {
			maxHistory = 2
		}
	}
	return &MetricsCollector{
		app:         app,
		maxHistory:  maxHistory,
		prevStreams: map[string]streamSample{},
	}
}

func (m *MetricsCollector) Start() {
	if m == nil || !m.app.Config().Metrics.Enabled {
		return
	}
	m.ctx, m.cancel = context.WithCancel(m.app.ctx)
	m.sample()
	m.wg.Add(1)
	go m.loop(time.Duration(m.app.Config().Metrics.SampleIntervalSeconds) * time.Second)
}

func (m *MetricsCollector) Stop() {
	if m == nil {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

func (m *MetricsCollector) loop(interval time.Duration) {
	defer m.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.sample()
		}
	}
}

func (m *MetricsCollector) sample() {
	now := time.Now()
	cpu, _ := readCPUTimes()
	memUsed, memTotal, _ := readMemInfo()
	diskFree, diskTotal := diskUsageBytes(m.app.Config().Recording.OutputDir)
	rx, tx, _ := readNetTotals()
	load := readLoadAvg()
	pids := m.app.recorder.ProcessSnapshot()
	statuses := m.app.recorder.Statuses()

	type rawProcess struct {
		pid   int
		ticks uint64
		rss   uint64
	}
	raws := make(map[string]rawProcess, len(pids))
	for id, pid := range pids {
		ticks, err := readProcessTicks(pid)
		if err != nil {
			continue
		}
		rss, _ := readProcessRSS(pid)
		raws[id] = rawProcess{pid: pid, ticks: ticks, rss: rss}
	}

	var backlog int64
	if m.app.uploader != nil {
		if value, err := m.app.uploader.BacklogBytes(); err == nil {
			backlog = value
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	elapsed := 0.0
	if m.hasPrev && !m.prevTime.IsZero() {
		elapsed = now.Sub(m.prevTime).Seconds()
	}

	point := MetricPoint{
		Time:             now,
		MemoryUsedBytes:  memUsed,
		MemoryTotalBytes: memTotal,
		DiskFreeBytes:    diskFree,
		DiskTotalBytes:   diskTotal,
		ActiveStreams:    countRunning(statuses),
		BacklogBytes:     backlog,
		Load1:            load,
	}
	if elapsed > 0 {
		point.CPUPercent = cpuPercent(m.prevCPU, cpu)
		point.NetRxBytesPerSec = float64(deltaUint(m.prevNet.rx, rx)) / elapsed
		point.NetTxBytesPerSec = float64(deltaUint(m.prevNet.tx, tx)) / elapsed
	}

	cpuCount := runtime.NumCPU()
	if cpuCount < 1 {
		cpuCount = 1
	}

	streams := make([]StreamMetrics, 0, len(statuses))
	nextPrev := make(map[string]streamSample, len(statuses))
	for id, status := range statuses {
		if !status.Running {
			continue
		}
		metric := StreamMetrics{
			ID:          id,
			BytesTotal:  status.BytesTotal,
			ChunksTotal: status.ChunksTotal,
			LastChunkAt: status.LastChunkAt,
		}
		previous := m.prevStreams[id]
		if raw, ok := raws[id]; ok {
			metric.PID = raw.pid
			metric.MemoryBytes = raw.rss
			if previous.pid == raw.pid && elapsed > 0 && raw.ticks >= previous.ticks {
				metric.CPUPercent = float64(raw.ticks-previous.ticks) /
					float64(procClockTicksPerSecond) * 100 / float64(cpuCount) / elapsed
			}
			nextPrev[id] = streamSample{pid: raw.pid, ticks: raw.ticks}
		}
		if elapsed > 0 && previous.pid == metric.PID && previous.pid != 0 {
			if status.BytesTotal >= previous.bytes {
				metric.OutputBytesPerSec = float64(status.BytesTotal-previous.bytes) / elapsed
			}
			if status.ChunksTotal >= previous.chunks {
				metric.ChunksPerMinute = float64(status.ChunksTotal-previous.chunks) / elapsed * 60
			}
		}
		entry := nextPrev[id]
		entry.bytes = status.BytesTotal
		entry.chunks = status.ChunksTotal
		nextPrev[id] = entry
		streams = append(streams, metric)
	}

	m.prevStreams = nextPrev
	m.prevCPU = cpu
	m.prevNet = netTimes{rx: rx, tx: tx}
	m.prevTime = now
	m.hasPrev = true
	m.current = &point
	m.streams = streams
	m.history = append(m.history, point)
	if m.maxHistory > 0 && len(m.history) > m.maxHistory {
		m.history = m.history[len(m.history)-m.maxHistory:]
	}
}

func (m *MetricsCollector) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{
			History:  []MetricPoint{},
			Streams:  []StreamMetrics{},
			Capacity: insufficientCapacity("Collecting system metrics"),
		}
	}
	cfg := m.app.Config()
	snapshot := MetricsSnapshot{
		Enabled:         cfg.Metrics.Enabled,
		IntervalSeconds: cfg.Metrics.SampleIntervalSeconds,
		History:         []MetricPoint{},
		Streams:         []StreamMetrics{},
	}

	m.mu.RLock()
	if m.current != nil {
		current := *m.current
		snapshot.Current = &current
	}
	snapshot.History = append(snapshot.History, m.history...)
	snapshot.Streams = append(snapshot.Streams, m.streams...)
	m.mu.RUnlock()

	snapshot.Capacity = calculateCapacity(cfg.Capacity, snapshot.Current, snapshot.History, snapshot.Streams)
	return snapshot
}

func (m *MetricsCollector) WarningsForStream(stream StreamConfig) []string {
	if m == nil || !m.app.Config().Metrics.Enabled {
		return nil
	}
	cfg := m.app.Config()
	snapshot := m.Snapshot()
	if snapshot.Current == nil {
		return nil
	}

	var warnings []string
	if snapshot.Capacity.Status == "at_capacity" {
		warnings = append(warnings, fmt.Sprintf(
			"server is at capacity (%s is the bottleneck); starting this stream may degrade active recordings",
			snapshot.Capacity.Bottleneck))
	}

	if cfg.Capacity.NetworkMbps > 0 {
		limitBytes := cfg.Capacity.NetworkMbps * 1000000 / 8
		headroom := limitBytes*cfg.Capacity.NetworkTargetPercent/100 -
			(snapshot.Current.NetRxBytesPerSec + snapshot.Current.NetTxBytesPerSec)
		requested := streamBitrateBits(stream) / 8
		if headroom < requested {
			warnings = append(warnings, fmt.Sprintf(
				"requested stream bitrate %.0f kbps exceeds the remaining configured network headroom of %.0f kbps",
				requested*8/1000, headroom*8/1000))
		}
	}

	if stream.Video != nil && videoEnabled(*stream.Video) && snapshot.Capacity.Confidence == "assumed" {
		warnings = append(warnings, "video-enabled streams are much more CPU and bandwidth intensive than audio-only streams; capacity is currently estimated from assumptions")
	}
	return dedupeStrings(warnings)
}

func calculateCapacity(cfg CapacityConfig, current *MetricPoint, history []MetricPoint, streams []StreamMetrics) CapacityEstimate {
	if current == nil {
		return insufficientCapacity("Collecting system metrics")
	}

	active := current.ActiveStreams
	estimate := CapacityEstimate{
		Status:        "ok",
		Confidence:    "assumed",
		ActiveStreams: active,
		Resources:     []ResourceHeadroom{},
	}

	var cpuSum, memorySum, outputSum float64
	var cpuCount, memoryCount, outputCount int
	measured := false
	for _, stream := range streams {
		if stream.PID == 0 {
			continue
		}
		cpuSum += stream.CPUPercent
		cpuCount++
		memorySum += float64(stream.MemoryBytes)
		memoryCount++
		if stream.OutputBytesPerSec > 0 {
			outputSum += stream.OutputBytesPerSec
			outputCount++
		}
		if stream.CPUPercent > 0 || stream.MemoryBytes > 0 {
			measured = true
		}
	}
	if measured && len(history) >= 2 && active > 0 {
		estimate.Confidence = "measured"
	}

	cpuPerStream := cfg.AssumedCPUPerStreamPercent
	if cpuCount > 0 && cpuSum > 0 {
		cpuPerStream = cpuSum / float64(cpuCount)
	}
	memoryPerStream := cfg.AssumedMemoryPerStreamMB * 1024 * 1024
	if memoryCount > 0 && memorySum > 0 {
		memoryPerStream = memorySum / float64(memoryCount)
	}
	outputPerStream := 0.0
	if outputCount > 0 && outputSum > 0 {
		outputPerStream = outputSum / float64(outputCount)
	}

	cpuAdditional := metricsUnlimitedStreams
	if cpuPerStream > 0 {
		cpuAdditional = clampAdditional((cfg.CPUTargetPercent - current.CPUPercent) / cpuPerStream)
	}
	estimate.Resources = append(estimate.Resources, ResourceHeadroom{
		Name:              "CPU",
		Unit:              "percent",
		Used:              current.CPUPercent,
		Limit:             cfg.CPUTargetPercent,
		UsedPercent:       usagePercent(current.CPUPercent, cfg.CPUTargetPercent),
		AdditionalStreams: cpuAdditional,
	})

	memoryLimitBytes := float64(current.MemoryTotalBytes) * cfg.MemoryTargetPercent / 100
	memoryAdditional := metricsUnlimitedStreams
	if memoryPerStream > 0 && memoryLimitBytes > 0 {
		memoryAdditional = clampAdditional((memoryLimitBytes - float64(current.MemoryUsedBytes)) / memoryPerStream)
	}
	estimate.Resources = append(estimate.Resources, ResourceHeadroom{
		Name:              "Memory",
		Unit:              "bytes",
		Used:              float64(current.MemoryUsedBytes),
		Limit:             memoryLimitBytes,
		UsedPercent:       usagePercent(float64(current.MemoryUsedBytes), memoryLimitBytes),
		AdditionalStreams: memoryAdditional,
	})

	if current.DiskTotalBytes > 0 {
		diskUsedBytes := uint64(0)
		if current.DiskTotalBytes > current.DiskFreeBytes {
			diskUsedBytes = current.DiskTotalBytes - current.DiskFreeBytes
		}
		diskLimitBytes := float64(current.DiskTotalBytes) * cfg.DiskTargetPercent / 100
		diskHeadroom := diskLimitBytes - float64(diskUsedBytes)
		freeHeadroom := float64(current.DiskFreeBytes) - cfg.MinFreeDiskGB*1e9
		if freeHeadroom < diskHeadroom {
			diskHeadroom = freeHeadroom
		}
		if diskHeadroom < 0 {
			diskHeadroom = 0
		}
		backlogGrowth := backlogGrowthBytesPerSec(history)
		diskAdditional := metricsUnlimitedStreams
		if diskHeadroom <= 0 {
			diskAdditional = 0
		} else if backlogGrowth > 1024 {
			perStreamGrowth := backlogGrowth
			if active > 0 {
				perStreamGrowth = backlogGrowth / float64(active)
			}
			sustainable := diskHeadroom / (cfg.DiskHorizonHours * 3600)
			if perStreamGrowth > 0 && sustainable < backlogGrowth {
				supported := sustainable / perStreamGrowth
				diskAdditional = clampAdditional(supported - float64(active))
			}
		}
		estimate.Resources = append(estimate.Resources, ResourceHeadroom{
			Name:              "Disk",
			Unit:              "bytes",
			Used:              float64(diskUsedBytes),
			Limit:             diskLimitBytes,
			UsedPercent:       usagePercent(float64(diskUsedBytes), diskLimitBytes),
			AdditionalStreams: diskAdditional,
		})
	}

	if cfg.NetworkMbps > 0 {
		networkLimitBytes := cfg.NetworkMbps * 1000000 / 8
		networkUsed := current.NetRxBytesPerSec + current.NetTxBytesPerSec
		perStreamNetwork := outputPerStream
		if active > 0 && networkUsed > 0 {
			perStreamNetwork = networkUsed / float64(active)
		}
		if perStreamNetwork <= 0 {
			perStreamNetwork = defaultStreamBitrate / 8
		}
		networkTarget := networkLimitBytes * cfg.NetworkTargetPercent / 100
		networkAdditional := clampAdditional((networkTarget - networkUsed) / perStreamNetwork)
		estimate.Resources = append(estimate.Resources, ResourceHeadroom{
			Name:              "Network",
			Unit:              "bytes_per_sec",
			Used:              networkUsed,
			Limit:             networkTarget,
			UsedPercent:       usagePercent(networkUsed, networkTarget),
			AdditionalStreams: networkAdditional,
		})
	}

	bottleneckIndex := -1
	recommended := metricsUnlimitedStreams
	for i := range estimate.Resources {
		if estimate.Resources[i].AdditionalStreams < recommended {
			recommended = estimate.Resources[i].AdditionalStreams
			bottleneckIndex = i
		}
	}
	if bottleneckIndex < 0 {
		return insufficientCapacity("Collecting system metrics")
	}
	for i := range estimate.Resources {
		estimate.Resources[i].Limiting = i == bottleneckIndex
	}
	estimate.RecommendedAdditionalStreams = recommended
	estimate.Bottleneck = estimate.Resources[bottleneckIndex].Name

	switch {
	case recommended <= 0:
		estimate.Status = "at_capacity"
		estimate.Message = fmt.Sprintf("At capacity: %s is the limiting resource. Starting another stream may degrade existing recordings.", estimate.Bottleneck)
	case recommended <= 2:
		estimate.Status = "warning"
		estimate.Message = fmt.Sprintf("Limited headroom: about %d more stream(s) fit before %s becomes the bottleneck.", recommended, estimate.Bottleneck)
	case recommended >= metricsUnlimitedStreams:
		estimate.Status = "ok"
		estimate.Message = fmt.Sprintf("Headroom is not limited by the configured thresholds; %s is the current bottleneck.", estimate.Bottleneck)
	default:
		estimate.Status = "ok"
		estimate.Message = fmt.Sprintf("Headroom for about %d more stream(s); %s is the current bottleneck.", recommended, estimate.Bottleneck)
	}
	return estimate
}

func insufficientCapacity(message string) CapacityEstimate {
	return CapacityEstimate{
		Status:     "insufficient",
		Confidence: "insufficient",
		Resources:  []ResourceHeadroom{},
		Message:    message,
	}
}

func countRunning(statuses map[string]WorkerStatus) int {
	count := 0
	for _, status := range statuses {
		if status.Running {
			count++
		}
	}
	return count
}

func clampAdditional(value float64) int {
	if value <= 0 {
		return 0
	}
	if value >= metricsUnlimitedStreams {
		return metricsUnlimitedStreams
	}
	return int(value)
}

func usagePercent(used, limit float64) float64 {
	if limit <= 0 {
		return 0
	}
	return used / limit * 100
}

func backlogGrowthBytesPerSec(history []MetricPoint) float64 {
	if len(history) < 2 {
		return 0
	}
	first := history[0]
	last := history[len(history)-1]
	elapsed := last.Time.Sub(first.Time).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(last.BacklogBytes-first.BacklogBytes) / elapsed
}

func deltaUint(previous, current uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func cpuPercent(previous, current cpuTimes) float64 {
	total := deltaUint(previous.total, current.total)
	idle := deltaUint(previous.idle, current.idle)
	if total == 0 {
		return 0
	}
	busy := total
	if idle < total {
		busy = total - idle
	}
	return float64(busy) / float64(total) * 100
}

func readCPUTimes() (cpuTimes, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	return parseCPUTimes(data)
}

func parseCPUTimes(data []byte) (cpuTimes, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		var times cpuTimes
		for i, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				continue
			}
			times.total += value
			if i == 3 || i == 4 {
				times.idle += value
			}
		}
		if times.total == 0 {
			return cpuTimes{}, fmt.Errorf("empty cpu counters")
		}
		return times, nil
	}
	return cpuTimes{}, fmt.Errorf("cpu line not found")
}

func readMemInfo() (uint64, uint64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	return parseMemInfo(data)
}

func parseMemInfo(data []byte) (uint64, uint64, error) {
	var total, available uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total = value * 1024
		case "MemAvailable":
			available = value * 1024
		}
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("MemTotal not found")
	}
	used := uint64(0)
	if total > available {
		used = total - available
	}
	return used, total, nil
}

func readNetTotals() (uint64, uint64, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	return parseNetDev(data)
}

func parseNetDev(data []byte) (uint64, uint64, error) {
	var rx, tx uint64
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		received, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		transmitted, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			continue
		}
		rx += received
		tx += transmitted
		found = true
	}
	if !found {
		return 0, 0, fmt.Errorf("no network interfaces found")
	}
	return rx, tx, nil
}

func readLoadAvg() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	return parseLoadAvg(data)
}

func parseLoadAvg(data []byte) float64 {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return value
}

func readProcessTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseProcessTicks(data)
}

func parseProcessTicks(data []byte) (uint64, error) {
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 >= len(text) {
		return 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 13 {
		return 0, fmt.Errorf("short process stat")
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}

func readProcessRSS(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	return parseProcessRSS(data)
}

func parseProcessRSS(data []byte) (uint64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return value * 1024, nil
	}
	return 0, fmt.Errorf("VmRSS not found")
}

func diskUsageBytes(path string) (uint64, uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	return free, total
}

func streamBitrateBits(stream StreamConfig) float64 {
	var bits float64
	if stream.Audio != nil {
		bits += parseBitrateBits(stream.Audio.Bitrate)
	}
	if stream.Video != nil && videoEnabled(*stream.Video) {
		bits += parseBitrateBits(stream.Video.Bitrate)
	}
	if bits <= 0 {
		return defaultStreamBitrate
	}
	return bits
}

func parseBitrateBits(value string) float64 {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "copy" {
		return 0
	}
	multiplier := 1.0
	switch {
	case strings.HasSuffix(value, "k"):
		multiplier = 1000
		value = strings.TrimSuffix(value, "k")
	case strings.HasSuffix(value, "m"):
		multiplier = 1000000
		value = strings.TrimSuffix(value, "m")
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return number * multiplier
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
