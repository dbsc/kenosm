package main

import "testing"

func TestDBTarget(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		wantHost   string
		wantLocal  bool
	}{
		{
			name:      "default localhost",
			url:       "postgres://kenosm:kenosm@localhost:5432/kenosm?sslmode=disable",
			wantHost:  "localhost:5432",
			wantLocal: true,
		},
		{
			name:      "loopback ip without port",
			url:       "postgres://kenosm:kenosm@127.0.0.1/kenosm",
			wantHost:  "127.0.0.1:5432",
			wantLocal: true,
		},
		{
			name:      "ipv6 loopback",
			url:       "postgres://kenosm:kenosm@[::1]:5433/kenosm",
			wantHost:  "[::1]:5433",
			wantLocal: true,
		},
		{
			name:      "remote host",
			url:       "postgres://kenosm:kenosm@db.internal.example:5432/kenosm",
			wantHost:  "db.internal.example:5432",
			wantLocal: false,
		},
		{
			name:      "malformed url",
			url:       "://not a url",
			wantHost:  "",
			wantLocal: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hostPort, local := dbTarget(c.url)
			if hostPort != c.wantHost {
				t.Fatalf("dbTarget(%q) hostPort = %q, want %q", c.url, hostPort, c.wantHost)
			}
			if local != c.wantLocal {
				t.Fatalf("dbTarget(%q) local = %v, want %v", c.url, local, c.wantLocal)
			}
		})
	}
}
