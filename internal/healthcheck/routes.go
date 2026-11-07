package healthcheck

import (
	"context"
	"sort"
	"sync"
)

// CheckRoutes 最多四路并发，整轮截止后把未完成项留为未知，不误报线路故障。
func CheckRoutes(ctx context.Context, targets map[string]int, probe func(context.Context, string) (int64, error)) []Route {
	tags := make([]string, 0, len(targets))
	for tag := range targets {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	routes := make([]Route, len(tags))
	jobs := make(chan int, len(tags))
	for i, tag := range tags {
		routes[i] = Route{Tag: tag, NodeID: targets[tag], Status: "incomplete", Error: "本轮检查未完成，请重试"}
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(4, len(tags)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				ms, err := probe(ctx, tags[i])
				routes[i].MS = ms
				if err != nil && ctx.Err() != nil {
					continue
				}
				routes[i].Status, routes[i].Error = "ok", ""
				if err != nil {
					routes[i].Status, routes[i].Error = "failed", err.Error()
				}
			}
		})
	}
	workers.Wait()
	return routes
}
