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
