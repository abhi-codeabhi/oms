package connector

import "sync"

// memRegistry is a goroutine-safe in-memory Registry implementation.
type memRegistry struct {
	mu  sync.RWMutex
	byID map[string]Connector
}

// NewRegistry returns an empty in-memory Registry.
func NewRegistry() Registry {
	return &memRegistry{byID: make(map[string]Connector)}
}

// Register adds (or replaces) a connector keyed by its manifest id.
func (r *memRegistry) Register(c Connector) {
	if c == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[c.Manifest().ID] = c
}

// Get returns the connector with id, if registered.
func (r *memRegistry) Get(id string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[id]
	return c, ok
}

// ByCapability returns all connectors declaring cap in their manifest.
func (r *memRegistry) ByCapability(cap Capability) []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Connector
	for _, c := range r.byID {
		for _, have := range c.Manifest().Capabilities {
			if have == cap {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
