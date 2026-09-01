package main

import "testing"

func TestImplementationVersion(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = ""
	if got := implementationVersion(); got != "dev" {
		t.Fatalf("empty version = %q, want dev", got)
	}
	version = "abc123"
	if got := implementationVersion(); got != "abc123" {
		t.Fatalf("injected version = %q, want abc123", got)
	}
}
