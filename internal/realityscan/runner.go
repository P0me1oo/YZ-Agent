package realityscan

import (
	"context"
	"sync"
	"time"
)

type Task struct {
	ID        string   `json:"id"`
	Targets   []string `json:"targets"`
	SNI       string   `json:"sni"`
	ExpiresAt int64    `json:"expires_at"`
}

type Report struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Error   string   `json:"error,omitempty"`
	Results []Result `json:"results,omitempty"`
}

// Runner 在控制请求之外运行，重复投递同一任务只返回原状态。
type Runner struct {
	mu     sync.Mutex
	report *Report
	lastID string
	cancel context.CancelFunc
	scan   func(context.Context, []string, string) ([]Result, error)
}

func NewRunner() *Runner { return &Runner{scan: (Scanner{}).Scan} }

func (r *Runner) Snapshot() *Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.report == nil {
		return nil
	}
	copy := *r.report
	return &copy
}

func (r *Runner) Accept(ctx context.Context, task *Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if task == nil {
		if r.cancel != nil {
			r.cancel()
		}
		r.report = nil
		return
	}
	if r.lastID == task.ID {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	if task.ID == "" || task.ExpiresAt <= time.Now().Unix() {
		return
	}
	deadline := time.Unix(task.ExpiresAt, 0)
	scanCtx, cancel := context.WithDeadline(ctx, deadline)
	r.cancel = cancel
	r.lastID = task.ID
	r.report = &Report{ID: task.ID, Status: "running"}
	go func() {
		defer cancel()
		results, err := r.scan(scanCtx, task.Targets, task.SNI)
		report := &Report{ID: task.ID, Status: "succeeded", Results: results}
		if err != nil {
			report.Status, report.Error = "failed", err.Error()
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.report != nil && r.report.ID == task.ID {
			r.report = report
		}
	}()
}
