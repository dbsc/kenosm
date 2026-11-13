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

func TestUSOnly(t *testing.T) {
	for _, s := range []string{"[('xkb','us')]", "[('xkb', 'us')]"} {
		if !usOnly(s) {
			t.Errorf("usOnly(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"[('xkb','us'), ('xkb','ru')]", "[('xkb','ru')]", ""} {
		if usOnly(s) {
			t.Errorf("usOnly(%q) = true, want false", s)
		}
	}
}

func TestKeySequence(t *testing.T) {
	cases := map[string]string{
		"return":  "28:1 28:0",
		"escape":  "1:1 1:0",
		"ctrl+c":  "29:1 46:1 46:0 29:0",
		"alt+Tab": "56:1 15:1 15:0 56:0",
		"shift+a": "42:1 30:1 30:0 42:0",
		"up":      "103:1 103:0",
	}
	for in, want := range cases {
		got, ok := keySequence(in)
		if !ok {
			t.Errorf("keySequence(%q) not ok", in)
			continue
		}
		if got != want {
			t.Errorf("keySequence(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "notakey", "ctrl+"} {
		if _, ok := keySequence(in); ok {
			t.Errorf("keySequence(%q) = ok, want not ok", in)
		}
	}
}
