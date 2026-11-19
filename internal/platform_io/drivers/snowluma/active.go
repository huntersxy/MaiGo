package snowluma

import "sync"

var (
	activeMu      sync.RWMutex
	activeAdapter *Adapter
)

func setActiveAdapter(adapter *Adapter) {
	activeMu.Lock()
	defer activeMu.Unlock()
	activeAdapter = adapter
}

func clearActiveAdapter(adapter *Adapter) {
	activeMu.Lock()
	defer activeMu.Unlock()
	if activeAdapter == adapter {
		activeAdapter = nil
	}
}

func ActiveAdapter() *Adapter {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return activeAdapter
}
