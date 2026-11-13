package desktop

import "testing"

func TestMouseButton(t *testing.T) {
	cases := map[string]string{
		"":       "1",
		"left":   "1",
		"Left":   "1",
		"middle": "2",
		"2":      "2",
		"right":  "3",
		"3":      "3",
		"bogus":  "1",
	}
	for in, want := range cases {
		if got := mouseButton(in); got != want {
			t.Errorf("mouseButton(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDigits(t *testing.T) {
	for _, s := range []string{"0", "123", "007"} {
		if !isDigits(s) {
			t.Errorf("isDigits(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "1a", "-1", "1.5", "#0", " 12"} {
		if isDigits(s) {
			t.Errorf("isDigits(%q) = true, want false", s)
		}
	}
}
