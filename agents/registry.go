package agents

import (
	"fmt"
	"sort"
	"sync"

	"google.golang.org/adk/v2/agent"
)

// factory is a function that constructs a fresh agent instance.
type factory func() (agent.Agent, error)

var (
	registryMu sync.RWMutex
	registry   = make(map[string]factory)
)

// Register adds an agent factory under the given name. It is called from
// agent package init() functions. Registering the same name twice panics,
// since it indicates a programming error.
func Register(name string, fn func() (agent.Agent, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[name]; ok {
		panic(fmt.Sprintf("agents: duplicate registration for %q", name))
	}
	registry[name] = fn
}

// Get returns a freshly constructed agent for the given name, or an error
// if the name is unknown.
func Get(name string) (agent.Agent, error) {
	registryMu.RLock()
	fn, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agents: unknown agent %q", name)
	}
	return fn()
}

// Names returns all registered agent names in sorted order.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
