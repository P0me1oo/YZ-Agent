package monitor

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxHostMemoryDistinguishesMissingAndZero(t *testing.T) {
	for _, tc := range []struct {
		name, proc  string
		total, used uint64
		valid       bool
	}{
		{"used", "MemTotal: 100 kB\nMemAvailable: 25 kB", 102400, 76800, true},
		{"idle", "MemAvailable: 100 kB\nMemTotal: 100 kB", 102400, 0, true},
		{"full", "MemTotal: 100 kB\nMemAvailable: 0 kB", 102400, 102400, true},
		{"unavailable", "MemTotal: 100 kB\nMemFree: 75 kB", 0, 0, false},
		{"invalid", "MemTotal: 100 kB\nMemAvailable: 101 kB", 0, 0, false},
		{"incomplete", "MemTotal: 100 kB\nMemAvailable:", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, used, ok := memoryUsage(bufio.NewScanner(strings.NewReader(tc.proc)))
			if total != tc.total || used != tc.used || ok != tc.valid {
				t.Fatalf("got total=%d used=%d valid=%t", total, used, ok)
			}
		})
	}
}

func TestLinuxPhysicalCounterRequiresCompleteSysfsReading(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "statistics"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{"ifindex": "3\n", "statistics/rx_bytes": "0\n", "statistics/tx_bytes": "90\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, ok := readPhysicalLink(dir)
	if !ok || c != (linkCounter{index: 3, rx: 0, tx: 90}) {
		t.Fatalf("invalid counter %+v", c)
	}
	if err := os.Remove(filepath.Join(dir, "statistics", "rx_bytes")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPhysicalLink(dir); ok {
		t.Fatal("incomplete readings cannot be used")
	}
}
