package machine

import (
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/service"
)

func TestOrchestratorAggregatesFailureRecoveryAndRemoval(t *testing.T) {
	orchestrator := &Orchestrator{
		nodeStatuses: make(map[int]service.RuntimeStatus),
	}
	var statuses []service.RuntimeStatus
	orchestrator.SetStatusHandler(func(status service.RuntimeStatus) {
		statuses = append(statuses, status)
	})

	orchestrator.setNodeStatus(1, service.RuntimeStarting)
	orchestrator.setNodeStatus(2, service.RuntimeStarting)
	orchestrator.setNodeStatus(1, service.RuntimeRunning)
	if got := statuses[len(statuses)-1]; got != service.RuntimeStarting {
		t.Fatalf("aggregate while node 2 starts = %q, want starting", got)
	}

	orchestrator.setNodeStatus(2, service.RuntimeFailed)
	if got := statuses[len(statuses)-1]; got != service.RuntimeFailed {
		t.Fatalf("aggregate after node failure = %q, want failed", got)
	}

	orchestrator.setNodeStatus(2, service.RuntimeStarting)
	orchestrator.setNodeStatus(2, service.RuntimeRunning)
	if got := statuses[len(statuses)-1]; got != service.RuntimeRunning {
		t.Fatalf("aggregate after recovery = %q, want running", got)
	}

	orchestrator.setNodeStatus(2, service.RuntimeFailed)
	orchestrator.reconcileNodeStatuses(map[int]panel.MachineNode{
		1: {ID: 1},
	})
	if got := statuses[len(statuses)-1]; got != service.RuntimeRunning {
		t.Fatalf("aggregate after failed node removal = %q, want running", got)
	}
}

func TestFinishNodeRemovesFailedHandleForRetry(t *testing.T) {
	done := make(chan struct{})
	orchestrator := &Orchestrator{
		nodes:        make(map[int]*nodeHandle),
		nodeStatuses: make(map[int]service.RuntimeStatus),
	}
	orchestrator.nodes[7] = &nodeHandle{done: done}

	orchestrator.finishNode(7, done, errors.New("start failed"))

	if _, ok := orchestrator.nodes[7]; ok {
		t.Fatal("failed node handle remained registered and would block rediscovery retry")
	}
	if got := orchestrator.nodeStatuses[7]; got != service.RuntimeFailed {
		t.Fatalf("failed node status = %q, want failed", got)
	}
}
