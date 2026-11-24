package handler

import (
	"testing"

	"kenosm/internal/swarm"
)

func TestSelectAgent(t *testing.T) {
	agents := []swarm.Agent{
		{Name: "Gemini", Slug: "gemini-1"},
		{Name: "DeepSeek A", Slug: "deepseek-1"},
		{Name: "DeepSeek B", Slug: "deepseek-2"},
	}

	got, ok := selectAgent(agents, "deepseek-2")
	if !ok {
		t.Fatal("expected a match")
	}
	if len(got) != 1 || got[0].Slug != "deepseek-2" {
		t.Fatalf("selectAgent narrowed to %+v, want single deepseek-2", got)
	}

	if _, ok := selectAgent(agents, "nope"); ok {
		t.Fatal("expected no match for unknown slug")
	}
}
