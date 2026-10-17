package httpapi

import "testing"

func TestNewestTimePrefersLaterValidTimestamp(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"2026-10-16T12:00:00Z", "2026-10-16T12:05:00.000000000Z", "2026-10-16T12:05:00Z"},
		{"2026-10-16T12:05:00Z", "2026-10-16T12:00:00.000000000Z", "2026-10-16T12:05:00Z"},
		{"", "2026-10-16T12:00:00.000000000Z", "2026-10-16T12:00:00Z"},
		{"2026-10-16T12:00:00Z", "", "2026-10-16T12:00:00Z"},
		{"bad", "also bad", ""},
	}
	for _, c := range cases {
		if got := newestTime(c.a, c.b); got != c.want {
			t.Fatalf("newestTime(%q,%q)=%q want %q", c.a, c.b, got, c.want)
		}
	}
}
