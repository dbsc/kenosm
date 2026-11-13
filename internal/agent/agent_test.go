package agent

import "testing"

func TestCleanRelPath(t *testing.T) {
	cases := map[string]string{
		"app/index.html":            "app/index.html",
		"workspace/app/index.html":  "app/index.html",
		"workspace/workspace/x.css": "x.css",
		"workspace":                 "",
		"./app/index.html":          "app/index.html",
		"/app/index.html":           "app/index.html",
		"dir\\file.txt":             "dir/file.txt",
		"  app/index.html  ":        "app/index.html",
		"workspace/":                "",
	}
	for in, want := range cases {
		if got := cleanRelPath(in); got != want {
			t.Errorf("cleanRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}
