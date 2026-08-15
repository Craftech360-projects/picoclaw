package main

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type liveKitWorkspaceLifecycle struct {
	WorkspaceIdentity string
	PreserveWorkspace bool
	DeviceMAC         string
	AgentID           string
	KidID             string
}

func resolveLiveKitWorkspaceLifecycle(
	roomName string,
	roomMetadata string,
	managerAPI config.LiveKitServiceManagerAPIConfig,
) liveKitWorkspaceLifecycle {
	deviceMAC, agentID := livekit.ResolvePersistenceFields(roomName, roomMetadata)
	kidID := livekit.ResolveKidID(roomMetadata)
	workspaceIdentity := strings.TrimSpace(roomName)
	managerBacked := managerAPIBaseURL(managerAPI) != "" && managerAPI.SessionStoreEnabled
	preserveWorkspace := true

	switch {
	// The workspace belongs to the Child, so it is named after them: the same
	// child on a replacement toy lands in the same directory, and a sibling on a
	// hand-me-down lands in a different one. This also disposes of the stale
	// directory a crashed teardown leaves behind — workspace-kid-7 is kid 7's
	// either way, so there is nothing left to leak across children.
	case kidID != "":
		workspaceIdentity = "kid-" + kidID
		preserveWorkspace = !managerBacked
	// Unpaired device, or a gateway that has not shipped the kid_id field yet.
	// Deploy order between the two repos must not matter.
	case deviceMAC != "":
		workspaceIdentity = "device-" + strings.ReplaceAll(deviceMAC, ":", "")
		preserveWorkspace = !managerBacked
	case strings.TrimSpace(agentID) != "":
		workspaceIdentity = "agent-" + routing.NormalizeAgentID(agentID)
		preserveWorkspace = true
	}
	if workspaceIdentity == "" {
		workspaceIdentity = "main"
	}

	return liveKitWorkspaceLifecycle{
		WorkspaceIdentity: workspaceIdentity,
		PreserveWorkspace: preserveWorkspace,
		DeviceMAC:         deviceMAC,
		AgentID:           agentID,
		KidID:             kidID,
	}
}

// workspaceIdentityFromOwnerKey turns the Manager's owner key into the directory
// identity for this session: "kid:1" -> "kid-1", "mac:00:16:3e:7a:11:c4" ->
// "device-00163e7a11c4".
//
// The two spellings name the same fact — whose workspace this is — and the
// Manager's is the one the artifacts are actually stored under. Deriving the
// directory from it means the folder a failed restore falls back to always
// belongs to the same owner the upload would have gone to. Room metadata cannot
// answer this reliably: it comes from a separately deployed gateway, and a
// dispatch that omits kid_id silently produces a MAC-named folder for a paired
// child.
//
// Returns "" for an unrecognised or empty key, which means "keep whatever the
// caller already worked out".
func workspaceIdentityFromOwnerKey(ownerKey string) string {
	key := strings.TrimSpace(ownerKey)
	switch {
	case strings.HasPrefix(key, "kid:"):
		kidID := strings.TrimSpace(strings.TrimPrefix(key, "kid:"))
		if kidID == "" {
			return ""
		}
		return "kid-" + kidID
	case strings.HasPrefix(key, "mac:"):
		mac := strings.TrimSpace(strings.TrimPrefix(key, "mac:"))
		if mac == "" {
			return ""
		}
		return "device-" + strings.ReplaceAll(mac, ":", "")
	default:
		return ""
	}
}
