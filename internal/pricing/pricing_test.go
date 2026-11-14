package pricing

import "testing"

func TestLookupExactAndFallback(t *testing.T) {
	tbl := DefaultTable()

	if p := tbl.Lookup("gemini-3.5-flash-lite"); p.Input != 0.075 || p.Output != 0.30 {
		t.Fatalf("lite exact = %+v, want {0.075 0.30}", p)
	}
	if p := tbl.Lookup("gemini-3.8-flash"); p.Input != 0.15 || p.Output != 0.60 {
		t.Fatalf("flash exact = %+v, want {0.15 0.60}", p)
	}
	// Pattern fallbacks for names not in the table.
	if p := tbl.Lookup("gemini-3.9-pro"); p.Input != 1.25 || p.Output != 10.0 {
		t.Fatalf("pro fallback = %+v, want {1.25 10.0}", p)
	}
	if p := tbl.Lookup("some-lite"); p.Input != 0.075 || p.Output != 0.30 {
		t.Fatalf("lite fallback = %+v, want {0.075 0.30}", p)
	}
	if p := tbl.Lookup("unknown-model"); p.Input != 0.15 || p.Output != 0.60 {
		t.Fatalf("flash fallback = %+v, want {0.15 0.60}", p)
	}
}

func TestThoughtDefaultsToOutput(t *testing.T) {
	tbl := DefaultTable()
	p := tbl.Lookup("gemini-3.5-flash")
	if p.Thought != p.Output {
		t.Fatalf("thought = %v, want output price %v", p.Thought, p.Output)
	}
}

func TestCost(t *testing.T) {
	tbl := DefaultTable()
	// 1M input + 1M output on flash: 0.15 + 0.60 = 0.75 USD.
	got := tbl.Cost("gemini-3.5-flash", 1_000_000, 1_000_000, 0)
	if got < 0.7499 || got > 0.7501 {
		t.Fatalf("Cost = %v, want ~0.75", got)
	}
	// 1M thought tokens billed at output rate.
	got = tbl.Cost("gemini-3.5-flash", 0, 0, 1_000_000)
	if got < 0.5999 || got > 0.6001 {
		t.Fatalf("thought Cost = %v, want ~0.60", got)
	}
}

func TestParseOverrides(t *testing.T) {
	tbl, err := ParseOverrides(`{"gemini-3.8-flash":{"input":0.1,"output":0.4}}`)
	if err != nil {
		t.Fatalf("ParseOverrides: %v", err)
	}
	if p := tbl.Lookup("gemini-3.8-flash"); p.Input != 0.1 || p.Output != 0.4 {
		t.Fatalf("override = %+v, want {0.1 0.4}", p)
	}
	if _, err := ParseOverrides("not json"); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
