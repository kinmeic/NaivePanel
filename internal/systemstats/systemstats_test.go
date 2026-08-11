package systemstats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProcStats(t *testing.T) {
	total, idle, err := parseCPUStat([]byte("cpu  100 20 30 400 50 6 7 8 900 1000\ncpu0 1 2 3 4\n"))
	if err != nil || total != 621 || idle != 450 {
		t.Fatalf("cpu total=%d idle=%d err=%v", total, idle, err)
	}
	memTotal, memUsed, err := parseMemInfo([]byte(
		"MemTotal:       1000 kB\nMemFree: 100 kB\nMemAvailable: 250 kB\n",
	))
	if err != nil || memTotal != 1000*1024 || memUsed != 750*1024 {
		t.Fatalf("memory total=%d used=%d err=%v", memTotal, memUsed, err)
	}
	load1, load5, load15, err := parseLoadAvg([]byte("0.25 0.50 1.75 1/100 123\n"))
	if err != nil || load1 != 0.25 || load5 != 0.50 || load15 != 1.75 {
		t.Fatalf("load averages=%v/%v/%v err=%v", load1, load5, load15, err)
	}
	uptime, err := parseUptime([]byte("90061.75 100.00\n"))
	if err != nil || uptime != 90061*time.Second+750*time.Millisecond {
		t.Fatalf("uptime=%v err=%v", uptime, err)
	}
	if got := parseOSRelease([]byte("NAME=Linux\nPRETTY_NAME=\"Example Linux 1.0\"\n")); got != "Example Linux 1.0" {
		t.Fatalf("os release=%q", got)
	}
}

func TestParseDiskAndNetwork(t *testing.T) {
	disk := []byte("   8       0 sda 10 0 100 0 20 0 300 0 0 0 0 0\n" +
		"   8       1 sda1 10 0 90 0 20 0 250 0 0 0 0 0\n")
	read, written, err := parseDiskStats(disk, func(name string) bool { return name == "sda" })
	if err != nil || read != 100*512 || written != 300*512 {
		t.Fatalf("disk read=%d written=%d err=%v", read, written, err)
	}
	netData := []byte("Inter-| Receive | Transmit\n face |\n" +
		" lo: 100 0 0 0 0 0 0 0 100 0 0 0 0 0 0 0\n" +
		" eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n" +
		" eth1: 3000 0 0 0 0 0 0 0 4000 0 0 0 0 0 0 0\n")
	rx, tx, err := parseNetDev(netData)
	if err != nil || rx != 4000 || tx != 6000 {
		t.Fatalf("network rx=%d tx=%d err=%v", rx, tx, err)
	}
}

func TestSamplerRates(t *testing.T) {
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	sys := filepath.Join(root, "sys")
	if err := os.MkdirAll(filepath.Join(proc, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sys, "class", "block", "sda"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(proc, "stat"), "cpu 100 0 100 800 0 0 0 0\n")
	write(filepath.Join(proc, "loadavg"), "0.10 0.20 0.30 1/100 123\n")
	write(filepath.Join(proc, "meminfo"), "MemTotal: 1000 kB\nMemAvailable: 500 kB\n")
	write(filepath.Join(proc, "diskstats"), "8 0 sda 1 0 100 0 1 0 100 0 0 0 0\n")
	write(filepath.Join(proc, "net", "dev"), "eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n")

	now := time.Unix(100, 0)
	s := &Sampler{procRoot: proc, sysRoot: sys, diskPath: root, now: func() time.Time { return now }}
	first := s.Sample()
	if !first.CPUAvailable || !first.LoadAvailable || !first.MemoryAvailable || !first.IOAvailable || !first.NetworkAvailable {
		t.Fatalf("first sample unavailable: %+v", first)
	}
	if first.Load1 != 0.10 || first.Load5 != 0.20 || first.Load15 != 0.30 {
		t.Fatalf("unexpected load averages: %+v", first)
	}

	write(filepath.Join(proc, "stat"), "cpu 150 0 150 900 0 0 0 0\n")
	write(filepath.Join(proc, "diskstats"), "8 0 sda 1 0 120 0 1 0 140 0 0 0 0\n")
	write(filepath.Join(proc, "net", "dev"), "eth0: 3000 0 0 0 0 0 0 0 5000 0 0 0 0 0 0 0\n")
	now = now.Add(2 * time.Second)
	second := s.Sample()
	if second.CPUPercent != 50 {
		t.Fatalf("cpu percent=%v", second.CPUPercent)
	}
	if second.DiskReadBPS != 20*512/2 || second.DiskWriteBPS != 40*512/2 {
		t.Fatalf("disk rates read=%v write=%v", second.DiskReadBPS, second.DiskWriteBPS)
	}
	if second.NetworkRxBPS != 1000 || second.NetworkTxBPS != 1500 {
		t.Fatalf("network rates rx=%v tx=%v", second.NetworkRxBPS, second.NetworkTxBPS)
	}
}
