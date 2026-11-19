package main

import (
	"os"
	"strings"
	"testing"
)

func TestExtractClaudeAuthURL(t *testing.T) {
	var tail strings.Builder
	chunk := []byte("\x1b[32mOpen this URL:\r\nhttps://claude.ai/oauth/authorize?\r\nclient_id=abc&state=xyz\x1b[0m\r\n")
	got := extractClaudeAuthURL(&tail, chunk)
	want := "https://claude.ai/oauth/authorize?client_id=abc&state=xyz"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if isClaudeAuthURL("https://evil.example.com/oauth/authorize?a=1") {
		t.Fatal("non-Anthropic host must not match")
	}
}

func TestStripHostSecretsAndOverrides(t *testing.T) {
	t.Setenv("AGENT_VM_UNSAFE_OPEN_NETWORK", "1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:7890")
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	stripHostSecretsAndOverrides()
	for _, k := range []string{"AGENT_VM_UNSAFE_OPEN_NETWORK", "HTTP_PROXY", "ANTHROPIC_API_KEY"} {
		if os.Getenv(k) != "" {
			t.Fatalf("expected %s to be unset", k)
		}
	}
}
