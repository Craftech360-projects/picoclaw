package main

import (
	"sort"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
)

var liveKitRequiredWorkspaceFileTools = []string{"read_file", "write_file", "list_dir"}

func ensureLiveKitWorkspaceFileTools(
	agentInstance *agent.AgentInstance,
	defaults *config.AgentDefaults,
	cfg *config.Config,
) []string {
	if agentInstance == nil || agentInstance.Tools == nil {
		return nil
	}

	missingBefore := make(map[string]struct{}, len(liveKitRequiredWorkspaceFileTools))
	for _, name := range liveKitRequiredWorkspaceFileTools {
		if _, ok := agentInstance.Tools.Get(name); !ok {
			missingBefore[name] = struct{}{}
		}
	}

	agent.RegisterWorkspaceTools(
		agentInstance.Tools,
		agentInstance.Workspace,
		defaults,
		cfg,
		agent.WorkspaceToolRegistrationOptions{
			ForceFileTools:   true,
			ReplaceFileTools: true,
		},
	)

	added := make([]string, 0, len(missingBefore))
	for name := range missingBefore {
		if _, ok := agentInstance.Tools.Get(name); ok {
			added = append(added, name)
		}
	}
	sort.Strings(added)
	return added
}
