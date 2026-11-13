package browser

import "testing"

func TestSelectorFor(t *testing.T) {
	cases := map[string]string{
		"3":         `[data-kenosm="3"]`,
		" 12 ":      `[data-kenosm="12"]`,
		"#submit":   "#submit",
		".btn":      ".btn",
		"a.link":    "a.link",
		"":          "",
		"   ":       "",
		"text=Sign": "text=Sign",
	}
	for in, want := range cases {
		if got := SelectorFor(in); got != want {
			t.Errorf("SelectorFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDigits(t *testing.T) {
	for _, s := range []string{"0", "123", "007"} {
		if !isDigits(s) {
			t.Errorf("isDigits(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "1a", "-1", "1.5", "#0"} {
		if isDigits(s) {
			t.Errorf("isDigits(%q) = true, want false", s)
		}
	}
}
