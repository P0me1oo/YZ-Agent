package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/gofrs/uuid/v5"
)

func TestMachineMailboxRemovesLastUserFromKernel(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	users := []model.UserSpec{{ID: 1}}
	s.updateUserState(users)
	s.machineMailbox = controlplane.NewNodeMailbox()
	s.machineMailbox.SeedBaseline(users, s.lastConfig)
	s.machineMailbox.MarkReady()
	k.onUpdateUsers = func(got []model.UserSpec) {
		if got == nil || len(got) != 0 {
			t.Fatal("内核未收到明确的空用户集")
		}
	}
	for range 2 {
		s.machineMailbox.Apply(controlplane.Event{
			Type: controlplane.EventSyncUserDelta, DeltaAction: "remove", DeltaUsers: users,
		})
		s.drainMachineMailbox(context.Background())
	}
	if len(s.lastUsers) != 0 || k.updateCalls != 1 {
		t.Fatalf("删除最后一个用户未生效或重复应用: users=%d, updates=%d", len(s.lastUsers), k.updateCalls)
	}
}

// 使用真实 ETag 请求验证失败应用之后仍能重新获取同一份用户数据。
func TestUserApplyFailureRetriesRESTSnapshot(t *testing.T) {
	for _, mode := range []string{"update", "initial_start", "delta_add", "delta_uuid", "delta_remove"} {
		t.Run(mode, func(t *testing.T) {
			newUUID := func() string {
				id, err := uuid.NewV4()
				if err != nil {
					t.Fatal(err)
				}
				return id.String()
			}
			oldUsers := []model.UserSpec{{ID: 1, UUID: newUUID()}}
			newUsers := []model.UserSpec{{ID: 2, UUID: newUUID()}}
			if mode == "delta_uuid" {
				newUsers[0].ID = 1
			} else if mode == "delta_add" {
				newUsers = append(oldUsers[:1:1], newUsers...)
			} else if mode == "delta_remove" {
				newUsers = []model.UserSpec{}
			}
			correctedUsers := append([]model.UserSpec(nil), newUsers...)
			correctedUsers = append(correctedUsers, model.UserSpec{ID: 99, UUID: newUUID()})
			var version atomic.Int32
			version.Store(1)
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") == `"config-v1"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("ETag", `"config-v1"`)
				_, _ = w.Write([]byte(`{"protocol":"vless","server_port":10001}`))
			})
			mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
				v := version.Load()
				etag := fmt.Sprintf(`"users-v%d"`, v)
				if r.Header.Get("If-None-Match") == etag {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("ETag", etag)
				users := oldUsers
				if v == 2 {
					users = newUsers
				} else if v >= 3 {
					users = correctedUsers
				}
				rows := make([]map[string]any, 0, len(users))
				for _, user := range users {
					rows = append(rows, map[string]any{"id": user.ID, "uuid": user.UUID})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"users": rows})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			cp := controlplane.NewPanelControlPlane(config.PanelConfig{URL: server.URL, NodeID: 1}, config.WSConfig{})
			ctx := context.Background()
			baseline, err := cp.Poll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			k := &fakeKernel{running: mode != "initial_start"}
			s := newTestService(k)
			s.source = cp
			s.lastConfig = baseline.Config
			s.lastConfigHash = computeConfigHash(baseline.Config)
			s.updateUserState(baseline.Users)
			version.Store(2)
			next, err := cp.Poll(ctx)
			if err != nil || next.Config != nil || next.Users == nil {
				t.Fatalf("仅用户变化的请求失败: %v", err)
			}
			failure := errors.New("测试应用失败")
			k.startErr, k.updateErr, k.addErr, k.removeErr = failure, failure, failure, failure
			switch mode {
			case "delta_add":
				s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncUserDelta, DeltaAction: "add", DeltaUsers: next.Users[1:]})
			case "delta_uuid":
				s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncUserDelta, DeltaAction: "add", DeltaUsers: next.Users})
			case "delta_remove":
				s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncUserDelta, DeltaAction: "remove", DeltaUsers: oldUsers})
			default:
				s.applyPullResult(ctx, pullResult{users: next.Users, userHash: computeUserHash(next.Users)})
			}
			if s.lastUserHash != computeUserHash(newUsers) {
				t.Fatalf("失败后未保留待修正用户状态: got=%q", s.lastUserHash)
			}
			calls := k.startCalls + k.updateCalls + k.addCalls + k.removeCalls
			k.startErr, k.updateErr, k.addErr, k.removeErr = nil, nil, nil, nil
			retry, err := cp.Poll(ctx)
			if err != nil {
				t.Fatalf("读取失败快照后的 REST 对账失败: %v", err)
			}
			if retry.Users != nil {
				s.applyPullResult(ctx, pullResult{users: retry.Users, userHash: computeUserHash(retry.Users)})
			}
			if got := k.startCalls + k.updateCalls + k.addCalls + k.removeCalls; got != calls {
				t.Fatalf("相同失败快照被后台再次应用: before=%d after=%d", calls, got)
			}

			version.Store(3)
			corrected, err := cp.Poll(ctx)
			if err != nil || corrected.Users == nil {
				t.Fatalf("修正后的用户快照未重新获取: err=%v", err)
			}
			s.applyPullResult(ctx, pullResult{users: corrected.Users, userHash: computeUserHash(corrected.Users)})
			if s.lastUserHash != computeUserHash(correctedUsers) || !k.running {
				t.Fatalf("修正后的用户快照未恢复运行: hash=%q running=%v", s.lastUserHash, k.running)
			}
			unchanged, err := cp.Poll(ctx)
			if err != nil || unchanged.Users != nil {
				t.Fatalf("成功后应恢复 ETag 去重: err=%v", err)
			}
		})
	}
}
