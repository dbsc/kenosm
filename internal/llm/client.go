package llm

import "context"

// Client is the minimal LLM backend the agent and swarm depend on. Gemini and
// any OpenAI-compatible Chat Completions backend (DeepSeek, etc.) both implement
// it, so a swarm can mix providers per agent.
type Client interface {
	Generate(ctx context.Context, system string, history []Turn, tools []Tool) (GenerateResult, error)
	Model() string
}
