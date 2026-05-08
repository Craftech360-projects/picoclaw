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
}

func resolveLiveKitWorkspaceLifecycle(
	roomName string,
	roomMetadata string,
	_ config.LiveKitServiceManagerAPIConfig,
) liveKitWorkspaceLifecycle {
	deviceMAC, agentID := livekit.ResolvePersistenceFields(roomName, roomMetadata)
	workspaceIdentity := strings.TrimSpace(roomName)
	preserveWorkspace := false

	switch {
	case deviceMAC != "":
		workspaceIdentity = "device-" + strings.ReplaceAll(deviceMAC, ":", "")
		preserveWorkspace = true
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
	}
}
