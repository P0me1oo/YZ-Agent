package main

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/P0me1oo/YZ-Agent/internal/service"
	"github.com/P0me1oo/YZ-Agent/internal/timesync"
)

type healthTracker struct {
	mu         sync.RWMutex
	components map[string]service.RuntimeStatus
	clock      *timesync.Manager
}

type healthSnapshot struct {
	Status   string            `json:"status"`
	Total    int               `json:"total"`
	Running  int               `json:"running"`
	Starting int               `json:"starting"`
	Failed   int               `json:"failed"`
	Clock    timesync.Snapshot `json:"clock"`

	componentsReady bool
}

func newHealthTracker() *healthTracker {
	return &healthTracker{components: make(map[string]service.RuntimeStatus)}
}

func (h *healthTracker) reset(componentIDs []string) {
	h.mu.Lock()
	h.components = make(map[string]service.RuntimeStatus, len(componentIDs))
	for _, id := range componentIDs {
		h.components[id] = service.RuntimeStarting
	}
	h.mu.Unlock()
}

func (h *healthTracker) set(componentID string, status service.RuntimeStatus) {
	h.mu.Lock()
	h.components[componentID] = status
	h.mu.Unlock()
}

func (h *healthTracker) setClock(clock *timesync.Manager) {
	h.mu.Lock()
	h.clock = clock
	h.mu.Unlock()
}

func (h *healthTracker) snapshot() healthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	snapshot := healthSnapshot{Total: len(h.components)}
	for _, status := range h.components {
		switch status {
		case service.RuntimeRunning:
			snapshot.Running++
		case service.RuntimeFailed:
			snapshot.Failed++
		default:
			snapshot.Starting++
		}
	}
	if h.clock != nil {
		snapshot.Clock = h.clock.Snapshot()
	} else {
		snapshot.Clock = timesync.Default().Snapshot()
	}
	snapshot.componentsReady = snapshot.Total > 0 && snapshot.Running == snapshot.Total
	switch {
	case snapshot.Failed > 0:
		snapshot.Status = "degraded"
	case !snapshot.componentsReady:
		snapshot.Status = "starting"
	case snapshot.Clock.Required && snapshot.Clock.Status != timesync.StatusNormal:
		snapshot.Status = "degraded"
	case snapshot.componentsReady:
		snapshot.Status = "ok"
	default:
		snapshot.Status = "starting"
	}
	return snapshot
}

func (h *healthTracker) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	snapshot := h.snapshot()
	w.Header().Set("Content-Type", "application/json")
	if !snapshot.componentsReady || snapshot.Failed > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(snapshot)
}
