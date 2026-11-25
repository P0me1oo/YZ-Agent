package monitor

import "testing"

func TestCongestionName(t *testing.T) {
	for _, tt := range []struct{ name, raw, want string }{{"bbr", "bbr\n", "bbr"}, {"cubic", "CUBIC", "cubic"}, {"unknown", "", ""}, {"malformed", "bbr cubic", ""}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := congestionName(tt.raw); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
