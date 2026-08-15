package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// The directory a failed restore falls back to must belong to the same owner the
// upload would have gone to, so this mapping is the load-bearing half of naming
// the workspace from the Manager's answer rather than from room metadata.
func TestWorkspaceIdentityFromOwnerKey(t *testing.T) {
	cases := []struct {
		name     string
		ownerKey string
		want     string
	}{
		{"paired child", "kid:1", "kid-1"},
		{"paired child, multi-digit", "kid:4231", "kid-4231"},
		{"unpaired device", "mac:00:16:3e:7a:11:c4", "device-00163e7a11c4"},
		{"surrounding whitespace", "  kid:7  ", "kid-7"},
		{"empty", "", ""},
		{"kid prefix with no id", "kid:", ""},
		{"mac prefix with no address", "mac:", ""},
		{"unknown scheme keeps the caller's guess", "owner:9", ""},
		// A Manager that predates the ownerKey field sends nothing at all; the
		// caller must be free to keep the identity it worked out from metadata.
		{"absent field", " ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspaceIdentityFromOwnerKey(tc.ownerKey); got != tc.want {
				t.Fatalf("workspaceIdentityFromOwnerKey(%q) = %q, want %q", tc.ownerKey, got, tc.want)
			}
		})
	}
}

// The owner key and the metadata-derived identity must agree on the unpaired
// case, or a device with no child would flip directories between the two
// sources for no reason.
func TestWorkspaceIdentityFromOwnerKeyMatchesMetadataForUnpairedDevice(t *testing.T) {
	var cfg config.LiveKitServiceManagerAPIConfig
	lifecycle := resolveLiveKitWorkspaceLifecycle(
		"room_00163E7A11C4_conversation",
		`{"device_mac":"00:16:3e:7a:11:c4"}`,
		cfg,
	)

	fromOwnerKey := workspaceIdentityFromOwnerKey("mac:00:16:3e:7a:11:c4")
	if fromOwnerKey != lifecycle.WorkspaceIdentity {
		t.Fatalf("owner key gave %q, room metadata gave %q — they must agree when no child is paired",
			fromOwnerKey, lifecycle.WorkspaceIdentity)
	}
}
