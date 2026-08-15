package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// The workspace directory is the child's, so the same child on a replacement toy
// must land in the same directory and a sibling on a hand-me-down must not.
func TestWorkspaceIdentityFollowsTheChild(t *testing.T) {
	var cfg config.LiveKitServiceManagerAPIConfig

	cases := []struct {
		name     string
		room     string
		metadata string
		want     string
	}{
		{
			name:     "paired device is named after the child",
			room:     "uuid_00163eacb538_conversation",
			metadata: `{"device_mac":"00:16:3E:AC:B5:38","kid_id":"77"}`,
			want:     "kid-77",
		},
		{
			name:     "same child on a different toy gets the same workspace",
			room:     "uuid_aabbccddeeff_conversation",
			metadata: `{"device_mac":"AA:BB:CC:DD:EE:FF","kid_id":"77"}`,
			want:     "kid-77",
		},
		{
			name:     "a sibling on the same toy gets a different workspace",
			room:     "uuid_00163eacb538_conversation",
			metadata: `{"device_mac":"00:16:3E:AC:B5:38","kid_id":"91"}`,
			want:     "kid-91",
		},
		{
			// The gateway and the worker deploy separately. A worker that ships
			// first sees metadata with no kid_id and must keep working.
			name:     "metadata without a kid falls back to the device",
			room:     "uuid_00163eacb538_conversation",
			metadata: `{"device_mac":"00:16:3E:AC:B5:38"}`,
			want:     "device-00163eacb538",
		},
		{
			name:     "an unpaired device sends a null kid and falls back",
			room:     "uuid_00163eacb538_conversation",
			metadata: `{"device_mac":"00:16:3E:AC:B5:38","kid_id":null}`,
			want:     "device-00163eacb538",
		},
		{
			name:     "an unquoted id is still a kid, not a silent fallback",
			room:     "uuid_00163eacb538_conversation",
			metadata: `{"device_mac":"00:16:3E:AC:B5:38","kid_id":77}`,
			want:     "kid-77",
		},
		{
			// This value becomes a directory component. Anything that is not a
			// bigint is discarded rather than sanitised into something plausible.
			name:     "a non-numeric kid id is refused rather than cleaned up",
			room:     "uuid_00163eacb538_conversation",
			metadata: `{"device_mac":"00:16:3E:AC:B5:38","kid_id":"../../etc"}`,
			want:     "device-00163eacb538",
		},
		{
			name:     "envelope-wrapped metadata still resolves the child",
			room:     "uuid_00163eacb538_conversation",
			metadata: `{"code":0,"data":{"device_mac":"00:16:3E:AC:B5:38","kid_id":"77"}}`,
			want:     "kid-77",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLiveKitWorkspaceLifecycle(tc.room, tc.metadata, cfg)
			if got.WorkspaceIdentity != tc.want {
				t.Fatalf("workspace identity = %q, want %q", got.WorkspaceIdentity, tc.want)
			}
		})
	}
}
