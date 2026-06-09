package livekit

import (
	"strings"
	"testing"
)

func TestLogContentPolicyRedactsSensitivePayloadsByDefault(t *testing.T) {
	policy := newLogContentPolicy(false, 0)
	secret := "my door code is 1234"

	if got := policy.contentPreview("livekit:device:a", secret, 240); got != redactedLogValue {
		t.Fatalf("contentPreview() = %q, want redacted value", got)
	}

	gotArgs := policy.toolArgsPreview("livekit:device:a", map[string]any{
		"path":    "/root/USER.md",
		"content": secret,
	}, 240)
	if gotArgs != redactedLogValue {
		t.Fatalf("toolArgsPreview() = %q, want redacted value", gotArgs)
	}
	if strings.Contains(gotArgs, "1234") {
		t.Fatalf("toolArgsPreview leaked sensitive argument: %q", gotArgs)
	}
}

func TestLogContentPolicyAllowsExplicitDetailedTrace(t *testing.T) {
	policy := newLogContentPolicy(true, 0)
	secret := "my door code is 1234"

	if got := policy.contentPreview("livekit:device:a", secret, 240); got != secret {
		t.Fatalf("contentPreview() = %q, want raw preview when detailed trace enabled", got)
	}

	gotArgs := policy.toolArgsPreview("livekit:device:a", map[string]any{"content": secret}, 240)
	if !strings.Contains(gotArgs, secret) {
		t.Fatalf("toolArgsPreview() = %q, want raw args when detailed trace enabled", gotArgs)
	}
}

func TestLogContentPolicyAllowsSampledDetailedTrace(t *testing.T) {
	policy := newLogContentPolicy(false, 1)
	secret := "sampled transcript"

	if got := policy.contentPreview("livekit:device:a", secret, 240); got != secret {
		t.Fatalf("contentPreview() = %q, want raw preview when sample rate is 1", got)
	}
}

func TestLogContentPolicyClampsSampleRate(t *testing.T) {
	if got := newLogContentPolicy(false, -0.5).sampleRate; got != 0 {
		t.Fatalf("negative sample rate = %f, want 0", got)
	}
	if got := newLogContentPolicy(false, 2).sampleRate; got != 1 {
		t.Fatalf("sample rate > 1 = %f, want 1", got)
	}
}
