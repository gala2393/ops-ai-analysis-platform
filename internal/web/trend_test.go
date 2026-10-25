package web

import "testing"

func TestInstanceRegexp(t *testing.T) {
	// The query text must carry doubled backslashes: the PromQL string parser
	// turns \\ into \ so the regex engine sees \. and only dots are escaped.
	got := instanceRegexp("10.20.1.47")
	want := `10\\.20\\.1\\.47`
	if got != want {
		t.Errorf("instanceRegexp = %q, want %q", got, want)
	}
}
