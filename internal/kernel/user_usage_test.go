package kernel

import (
	"fmt"
	"testing"
)

func TestUserUsageIPBoundsAndDedup(t *testing.T) {
	row := UserConnections{}
	for _, source := range []string{"8.8.8.8", "::ffff:8.8.8.8", "192.0.2.1", "127.0.0.1", "2606:4700:4700::1", "2606:4700:4700::2"} {
		row.AddAddress(source)
	}
	if got := len(row.Addresses()); got != 3 {
		t.Fatal(got)
	}
	for i := 0; i < 256; i++ {
		row.AddAddress(fmt.Sprintf("8.8.4.%d", i))
	}
	if row.Addresses() != nil {
		t.Fatal("truncated list looked complete")
	}
}
