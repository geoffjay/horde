package server

import (
	"sort"

	"github.com/geoffjay/horde/agents"
)

// AvailableAgentInfo describes an agent type that can be spawned: a built-in
// ADK agent from the registry, or a configured AAP agent definition.
type AvailableAgentInfo struct {
	Name string
	Kind AgentKind
}

// AvailableAgents returns the agent types this node can spawn — the built-in
// ADK registry agents plus the configured AAP agent definitions — sorted by
// name. A configured definition shadows a registry name of the same name.
func (s *Server) AvailableAgents() []AvailableAgentInfo {
	seen := make(map[string]bool)
	out := make([]AvailableAgentInfo, 0, len(s.cfg.AgentDefs))

	for name := range s.cfg.AgentDefs {
		kind := s.cfg.AgentDefs[name].Kind
		if kind == "" {
			kind = AgentKindADK
		}
		out = append(out, AvailableAgentInfo{Name: name, Kind: kind})
		seen[name] = true
	}
	for _, name := range agents.Names() {
		if seen[name] {
			continue
		}
		out = append(out, AvailableAgentInfo{Name: name, Kind: AgentKindADK})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
