package engine

import (
	"fmt"
	"sort"
	"sync"
)

var (
	mu       sync.RWMutex
	registry = map[string]Engine{}
)

// Register adds e to the registry under e.Name(). Typically called from
// engine package init() functions. Panics on duplicate registration.
func Register(e Engine) {
	mu.Lock()
	defer mu.Unlock()
	name := e.Name()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("engine: duplicate registration for %q", name))
	}
	registry[name] = e
}

// Get returns the registered Engine for name, or an error listing available
// engines if name is not found.
func Get(name string) (Engine, error) {
	mu.RLock()
	defer mu.RUnlock()
	if e, ok := registry[name]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("engine: unknown engine %q — registered: %v", name, registeredNames())
}

// registeredNames returns a sorted slice of registered engine names.
// Must be called with mu held (at least read-locked).
func registeredNames() []string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
