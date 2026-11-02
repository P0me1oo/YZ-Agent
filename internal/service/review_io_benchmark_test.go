package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkReviewPersistentTenThousandUsers(b *testing.B) {
	for _, active := range []bool{false, true} {
		name := "idle"
		if active {
			name = "single_change"
		}
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			var raw [32]byte
			if _, err := rand.Read(raw[:]); err != nil {
				b.Fatal(err)
			}
			sum := sha256.Sum256(raw[:])
			state, err := privacy.New([]privacy.Key{{ID: hex.EncodeToString(sum[:16]), Value: base64.StdEncoding.EncodeToString(raw[:])}})
			if err != nil {
				b.Fatal(err)
			}
			privacy.Bind(dir, state, nil, true)
			defer privacy.Bind(dir, nil, nil, true)
			store := newPendingReportStore(pendingTestConfig(dir, 7))
			pending := pendingTraffic{Traffic: make(map[int][2]int64, 10000)}
			for id := 1; id <= 10000; id++ {
				pending.Traffic[id] = [2]int64{1, 1}
			}
			if err := store.save(nil, pending); err != nil {
				b.Fatal(err)
			}
			var written int64
			previous, _ := os.Stat(store.path)
			seen := make(map[string]bool)
			b.ReportAllocs()
			b.ResetTimer()
			for sequence := range b.N {
				if active {
					pending.Traffic[1] = [2]int64{int64(sequence + 2), 1}
				}
				if err := store.save(nil, pending); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				current, _ := os.Stat(store.path)
				if current != nil && !os.SameFile(previous, current) {
					written += current.Size()
				}
				previous = current
				entries, _ := os.ReadDir(store.path + ".journal")
				for _, entry := range entries {
					if !seen[entry.Name()] {
						info, _ := os.Stat(filepath.Join(store.path+".journal", entry.Name()))
						written += info.Size()
						seen[entry.Name()] = true
					}
				}
				b.StartTimer()
			}
			b.ReportMetric(float64(written)/float64(b.N), "written-B/op")
		})
	}
}
