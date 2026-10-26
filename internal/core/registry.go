package core

import (
	"sort"
	"sync"
)

var (
	regMu    sync.RWMutex
	adapters = map[string]Adapter{}
)

// Register makes an adapter available. Adapter packages call it from init.
func Register(a Adapter) {
	regMu.Lock()
	defer regMu.Unlock()
	adapters[a.Name()] = a
}

// Lookup returns a registered adapter by name.
func Lookup(name string) (Adapter, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	a, ok := adapters[name]
	return a, ok
}

// Adapters returns all registered adapters sorted by name.
func Adapters() []Adapter {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
