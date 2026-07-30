// Package systemstats samples Linux host resource counters for the dashboard.
package systemstats

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Stats is one dashboard sample. Rate fields need two samples and therefore
// remain zero on the first request after the panel starts.
type Stats struct {
	SampledAt time.Time `json:"sampled_at"`

	CPUAvailable bool    `json:"cpu_available"`
	CPUPercent   float64 `json:"cpu_percent"`

	MemoryAvailable bool    `json:"memory_available"`
	MemoryTotal     uint64  `json:"memory_total"`
	MemoryUsed      uint64  `json:"memory_used"`
	MemoryPercent   float64 `json:"memory_percent"`

	DiskAvailable bool    `json:"disk_available"`
	DiskTotal     uint64  `json:"disk_total"`
	DiskUsed      uint64  `json:"disk_used"`
	DiskFree      uint64  `json:"disk_free"`
	DiskPercent   float64 `json:"disk_percent"`

	IOAvailable  bool    `json:"io_available"`
	DiskReadBPS  float64 `json:"disk_read_bps"`
	DiskWriteBPS float64 `json:"disk_write_bps"`

	NetworkAvailable bool    `json:"network_available"`
	NetworkRxBPS     float64 `json:"network_rx_bps"`
	NetworkTxBPS     float64 `json:"network_tx_bps"`
	NetworkRxTotal   uint64  `json:"network_rx_total"`
	NetworkTxTotal   uint64  `json:"network_tx_total"`

	Warnings []string `json:"warnings,omitempty"`
}

type counters struct {
	at          time.Time
	cpuOK       bool
	cpuTotal    uint64
	cpuIdle     uint64
	diskOK      bool
	diskRead    uint64
	diskWritten uint64
	networkOK   bool
	networkRx   uint64
	networkTx   uint64
}

// Sampler retains the previous counter sample so it can calculate rates.
type Sampler struct {
	mu       sync.Mutex
	procRoot string
	sysRoot  string
	diskPath string
	now      func() time.Time
	previous *counters
}

// New creates the production sampler.
func New() *Sampler {
	return &Sampler{
		procRoot: "/proc",
		sysRoot:  "/sys",
		diskPath: "/",
		now:      time.Now,
	}
}

// Sample reads current host counters. Individual sources fail independently
// so a missing /proc mount does not hide disk-capacity information.
func (s *Sampler) Sample() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	result := Stats{SampledAt: now}
	current := counters{at: now}

	if data, err := os.ReadFile(filepath.Join(s.procRoot, "stat")); err == nil {
		if total, idle, err := parseCPUStat(data); err == nil {
			current.cpuOK = true
			current.cpuTotal, current.cpuIdle = total, idle
			result.CPUAvailable = true
			if s.previous != nil && s.previous.cpuOK {
				totalDelta := delta(total, s.previous.cpuTotal)
				idleDelta := delta(idle, s.previous.cpuIdle)
				if totalDelta > 0 && idleDelta <= totalDelta {
					result.CPUPercent = 100 * float64(totalDelta-idleDelta) / float64(totalDelta)
				}
			}
		} else {
			result.Warnings = append(result.Warnings, "CPU: "+err.Error())
		}
	} else {
		result.Warnings = append(result.Warnings, "CPU 统计不可用")
	}

	if data, err := os.ReadFile(filepath.Join(s.procRoot, "meminfo")); err == nil {
		if total, used, err := parseMemInfo(data); err == nil {
			result.MemoryAvailable = true
			result.MemoryTotal, result.MemoryUsed = total, used
			if total > 0 {
				result.MemoryPercent = 100 * float64(used) / float64(total)
			}
		} else {
			result.Warnings = append(result.Warnings, "内存: "+err.Error())
		}
	} else {
		result.Warnings = append(result.Warnings, "内存统计不可用")
	}

	var fs unix.Statfs_t
	if err := unix.Statfs(s.diskPath, &fs); err == nil {
		blockSize := uint64(fs.Bsize)
		result.DiskTotal = fs.Blocks * blockSize
		result.DiskFree = fs.Bavail * blockSize
		freeIncludingReserved := fs.Bfree * blockSize
		if freeIncludingReserved <= result.DiskTotal {
			result.DiskUsed = result.DiskTotal - freeIncludingReserved
		}
		result.DiskAvailable = result.DiskTotal > 0
		usable := result.DiskUsed + result.DiskFree
		if usable > 0 {
			// Match df(1): reserved filesystem blocks are neither available
			// to services nor counted as used in the percentage denominator.
			result.DiskPercent = 100 * float64(result.DiskUsed) / float64(usable)
		}
	} else {
		result.Warnings = append(result.Warnings, "磁盘容量统计不可用")
	}

	if data, err := os.ReadFile(filepath.Join(s.procRoot, "diskstats")); err == nil {
		readBytes, writtenBytes, err := parseDiskStats(data, func(device string) bool {
			if strings.HasPrefix(device, "loop") || strings.HasPrefix(device, "ram") ||
				strings.HasPrefix(device, "fd") || strings.HasPrefix(device, "sr") ||
				strings.HasPrefix(device, "dm-") || strings.HasPrefix(device, "md") {
				return false
			}
			_, err := os.Stat(filepath.Join(s.sysRoot, "class", "block", device, "partition"))
			return os.IsNotExist(err)
		})
		if err == nil {
			current.diskOK = true
			current.diskRead, current.diskWritten = readBytes, writtenBytes
			result.IOAvailable = true
			if s.previous != nil && s.previous.diskOK {
				seconds := now.Sub(s.previous.at).Seconds()
				if seconds > 0 {
					result.DiskReadBPS = float64(delta(readBytes, s.previous.diskRead)) / seconds
					result.DiskWriteBPS = float64(delta(writtenBytes, s.previous.diskWritten)) / seconds
				}
			}
		} else {
			result.Warnings = append(result.Warnings, "磁盘 I/O: "+err.Error())
		}
	} else {
		result.Warnings = append(result.Warnings, "磁盘 I/O 统计不可用")
	}

	if data, err := os.ReadFile(filepath.Join(s.procRoot, "net", "dev")); err == nil {
		rx, tx, err := parseNetDev(data)
		if err == nil {
			current.networkOK = true
			current.networkRx, current.networkTx = rx, tx
			result.NetworkAvailable = true
			result.NetworkRxTotal, result.NetworkTxTotal = rx, tx
			if s.previous != nil && s.previous.networkOK {
				seconds := now.Sub(s.previous.at).Seconds()
				if seconds > 0 {
					result.NetworkRxBPS = float64(delta(rx, s.previous.networkRx)) / seconds
					result.NetworkTxBPS = float64(delta(tx, s.previous.networkTx)) / seconds
				}
			}
		} else {
			result.Warnings = append(result.Warnings, "网络: "+err.Error())
		}
	} else {
		result.Warnings = append(result.Warnings, "网络统计不可用")
	}

	s.previous = &current
	return result
}

func delta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func parseCPUStat(data []byte) (total, idle uint64, err error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	if !scanner.Scan() {
		return 0, 0, errors.New("缺少 cpu 行")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, errors.New("cpu 行格式异常")
	}
	values := make([]uint64, 0, len(fields)-1)
	for i, raw := range fields[1:] {
		value, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("cpu 计数格式异常")
		}
		values = append(values, value)
		// guest and guest_nice are already included in user and nice by
		// Linux, so adding fields after steal would double-count CPU time.
		if i < 8 {
			total += value
		}
	}
	idle = values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return total, idle, nil
}

func parseMemInfo(data []byte) (total, used uint64, err error) {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemTotal" && key != "MemAvailable" {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, 0, errors.New("meminfo 数值格式异常")
		}
		values[key] = value * 1024
	}
	total = values["MemTotal"]
	available, ok := values["MemAvailable"]
	if total == 0 || !ok || available > total {
		return 0, 0, errors.New("缺少有效的 MemTotal/MemAvailable")
	}
	return total, total - available, nil
}

func parseDiskStats(data []byte, include func(string) bool) (readBytes, writtenBytes uint64, err error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	found := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || !include(fields[2]) {
			continue
		}
		readSectors, readErr := strconv.ParseUint(fields[5], 10, 64)
		writtenSectors, writeErr := strconv.ParseUint(fields[9], 10, 64)
		if readErr != nil || writeErr != nil {
			return 0, 0, errors.New("diskstats 数值格式异常")
		}
		readBytes += readSectors * 512
		writtenBytes += writtenSectors * 512
		found = true
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if !found {
		return 0, 0, errors.New("未找到可统计的块设备")
	}
	return readBytes, writtenBytes, nil
}

func parseNetDev(data []byte) (rx, tx uint64, err error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		name, countersText, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		fields := strings.Fields(countersText)
		if len(fields) < 9 {
			return 0, 0, errors.New("net/dev 行格式异常")
		}
		received, rxErr := strconv.ParseUint(fields[0], 10, 64)
		transmitted, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			return 0, 0, errors.New("net/dev 数值格式异常")
		}
		rx += received
		tx += transmitted
		found = true
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if !found {
		return 0, 0, errors.New("未找到非回环网络接口")
	}
	return rx, tx, nil
}
