package main

import "testing"

func TestValidateHosting(t *testing.T) {
	tests := []struct {
		listen, origin, cert, key, want string
		valid                           bool
	}{
		{"127.0.0.1:8787", "", "", "", "http://127.0.0.1:8787", true},
		{"[::1]:8787", "", "", "", "http://[::1]:8787", true},
		{"127.0.0.1:8787", "https://fixture.example", "", "", "https://fixture.example", true},
		{"127.0.0.1:8787", "https://FIXTURE.example:443", "", "", "https://fixture.example", true},
		{"127.0.0.1:80", "", "", "", "http://127.0.0.1", true},
		{"0.0.0.0:8787", "https://fixture.example:8787", "fixture-cert", "fixture-key", "https://fixture.example:8787", true},
		{"127.0.0.1:8787", "", "fixture-cert", "fixture-key", "https://127.0.0.1:8787", true},
		{"0.0.0.0:8787", "", "", "", "", false},
		{"0.0.0.0:8787", "", "fixture-cert", "fixture-key", "", false},
		{"127.0.0.1:8787", "http://fixture.example", "", "", "", false},
		{"localhost:8787", "", "", "", "", false},
		{"127.0.0.1:0", "", "", "", "", false},
		{"127.0.0.1:8787", "", "fixture-cert", "", "", false},
		{"127.0.0.1:8787", "http://127.0.0.1:8787", "fixture-cert", "fixture-key", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.listen+tt.origin+tt.cert, func(t *testing.T) {
			got, e := ValidateHosting(tt.listen, tt.origin, tt.cert, tt.key)
			if (e == nil) != tt.valid || got != tt.want {
				t.Fatalf("got %q %v", got, e)
			}
		})
	}
}
func TestValidateHostingRejectsAmbiguousOrigin(t *testing.T) {
	for _, origin := range []string{"https://fixture.example/", "https://fixture.example?", "https://fixture.example#fragment", "https://user@fixture.example", "https://fixture.example:0", "https://fixture.example:70000", "https://fixture.example:", "https://0.0.0.0", "https://bad_name.example", "https://fixture.example/../../", "https://[fe80::1%25eth0]"} {
		if _, e := ValidateHosting("127.0.0.1:8787", origin, "", ""); e == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
}
