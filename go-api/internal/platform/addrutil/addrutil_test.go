package addrutil

import "testing"

func TestIsIPOrHostname(t *testing.T) {
	t.Parallel()

	cases := []struct {
		value string
		want  bool
	}{
		{value: "1.1.1.1", want: true},
		{value: "2400:c620:28:9be::1", want: true},
		{value: "entry-a.example.com", want: true},
		{value: "edge01", want: true},
		{value: "edge01.example.com.", want: true},
		{value: "https://example.com", want: false},
		{value: "example.com:443", want: false},
		{value: "-edge.example.com", want: false},
		{value: "edge..example.com", want: false},
		{value: "", want: false},
	}

	for _, tc := range cases {
		if got := IsIPOrHostname(tc.value); got != tc.want {
			t.Fatalf("IsIPOrHostname(%q)=%v want %v", tc.value, got, tc.want)
		}
	}
}
