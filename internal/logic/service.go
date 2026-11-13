// Package logic 实现车队日志配置发布与恢复的全部业务规则（题面 4.1~4.7 / R1~R8）。
// 所有写操作串行化（互斥锁 + 单连接 SQLite），保证并发下状态一致且 -race 通过。
package logic

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"fleet/internal/model"
	"fleet/internal/store"
)

// Service 业务服务。
type Service struct {
	st      *store.Store
	mu      sync.Mutex
	Timeout time.Duration
	nowFn   func() time.Time
}

// New 创建业务服务；timeout 为发布回执超时阈值（教学时钟可缩短，测试与演示走同一规则代码路径）。
func New(st *store.Store, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = model.DefaultTimeoutSecond * time.Second
	}
	return &Service{st: st, Timeout: timeout, nowFn: func() time.Time { return time.Now() }}
}

// SetNowFn 注入时钟（仅测试使用，用于超时/迟到场景）。
func (s *Service) SetNowFn(fn func() time.Time) { s.nowFn = fn }

func (s *Service) now() time.Time      { return s.nowFn() }
func (s *Service) nowStr() string      { return s.now().UTC().Format(time.RFC3339) }
func (s *Service) Store() *store.Store { return s.st }

// requireRole 角色校验（R8：未授权角色执行发布/审批等一律拒绝并留痕）。
func (s *Service) requireRole(ctx context.Context, u model.User, action string, roles ...model.Role) error {
	for _, r := range roles {
		if u.Is(r) {
			return nil
		}
	}
	err := model.ErrForbidden("FORBIDDEN_ROLE",
		"角色 %s 无权执行「%s」（该操作仅限指定角色）", u.Role, action)
	s.logEvent(ctx, u, action, "", false, err.Message, "", "")
	return err
}

// logEvent 关键动作与拒绝留痕（谁、何时、什么操作、原因）。
func (s *Service) logEvent(ctx context.Context, u model.User, action, target string,
	accepted bool, reason string, activityID, vehicleID string) {
	_ = s.st.InsertEvent(ctx, &model.Event{
		At: s.nowStr(), Actor: u.Username, Role: string(u.Role), Action: action,
		Target: target, Accepted: accepted, Reason: reason,
		ActivityID: activityID, VehicleID: vehicleID,
	})
}

func (s *Service) logSystemEvent(ctx context.Context, action, target, reason, activityID, vehicleID string) {
	_ = s.st.InsertEvent(ctx, &model.Event{
		At: s.nowStr(), Actor: "system", Role: "system", Action: action,
		Target: target, Accepted: true, Reason: reason,
		ActivityID: activityID, VehicleID: vehicleID,
	})
}

// logEventWithDetail 带结构化附加信息的留痕（如申请参数历史）。
func (s *Service) logEventWithDetail(ctx context.Context, u model.User, action, target string,
	accepted bool, reason, detail, activityID, vehicleID string) {
	_ = s.st.InsertEvent(ctx, &model.Event{
		At: s.nowStr(), Actor: u.Username, Role: string(u.Role), Action: action,
		Target: target, Accepted: accepted, Reason: reason, Detail: detail,
		ActivityID: activityID, VehicleID: vehicleID,
	})
}

// nextID 生成 APP-000001 / ACT-000001 形式的业务编号。
func (s *Service) nextID(ctx context.Context, prefix, seq string) (string, error) {
	n, err := s.st.NextSeq(ctx, seq)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%06d", prefix, n), nil
}

// mustGetApplication 读取申请或返回 404。
func (s *Service) mustGetApplication(ctx context.Context, id string) (*model.Application, error) {
	a, ok, err := s.st.Application(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, model.ErrNotFound("申请 %s 不存在", id)
	}
	return &a, nil
}

// mustGetActivity 读取活动或返回 404。
func (s *Service) mustGetActivity(ctx context.Context, id string) (*model.Activity, error) {
	a, ok, err := s.st.Activity(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, model.ErrNotFound("活动 %s 不存在", id)
	}
	return &a, nil
}

// joinIDs 以逗号拼接车辆编号（持久化格式）。
func joinIDs(ids []string) string { return strings.Join(ids, ",") }

// validateApplicationInput 校验申请要素：原因、五台目标车辆、间隔 10~300（R1）。
func (s *Service) validateApplicationInput(ctx context.Context, in model.ApplicationInput) (string, int, []string, error) {
	reason := trimSpace(in.Reason)
	if reason == "" {
		return "", 0, nil, model.ErrBadRequest("INVALID_REASON", "申请必须包含排查原因")
	}
	if in.Interval < model.IntervalMin || in.Interval > model.IntervalMax {
		return "", 0, nil, model.ErrBadRequest("INVALID_INTERVAL",
			"日志间隔必须在 %d~%d 秒之间（当前 %d）", model.IntervalMin, model.IntervalMax, in.Interval)
	}
	vehicles, err := s.st.Vehicles(ctx)
	if err != nil {
		return "", 0, nil, err
	}
	expected := make([]string, 0, len(vehicles))
	for _, v := range vehicles {
		expected = append(expected, v.ID)
	}
	got := make([]string, 0, len(in.VehicleIDs))
	seen := map[string]bool{}
	for _, id := range in.VehicleIDs {
		if seen[id] {
			return "", 0, nil, model.ErrBadRequest("INVALID_VEHICLES", "目标车辆重复：%s", id)
		}
		seen[id] = true
		got = append(got, id)
	}
	if len(got) != len(expected) || !sameSet(got, expected) {
		return "", 0, nil, model.ErrBadRequest("INVALID_VEHICLES",
			"目标车辆必须是预置五台 %v（当前 %v）", expected, got)
	}
	return reason, in.Interval, got, nil
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, x := range b {
		set[x] = true
	}
	for _, x := range a {
		if !set[x] {
			return false
		}
	}
	return true
}

// publishLocked 面向单台车下发一个新版本：仅写期望值，不改生效值（R3，下发 ≠ 生效）。
func (s *Service) publishLocked(ctx context.Context, act *model.Activity, vehicleID, kind string,
	value int, actor model.User) (int, error) {
	version, err := s.st.NextVersion(ctx)
	if err != nil {
		return 0, err
	}
	state, ok, err := s.st.VehicleState(ctx, vehicleID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, model.ErrNotFound("车辆 %s 不存在", vehicleID)
	}
	av, ok, err := s.st.ActivityVehicle(ctx, act.ID, vehicleID)
	if err != nil {
		return 0, err
	}
	if !ok {
		av = model.ActivityVehicle{
			ActivityID: act.ID, VehicleID: vehicleID,
			BaseValue: state.EffectiveValue, BaseVersion: state.EffectiveVersion,
		}
	}
	now := s.nowStr()
	av.Phase = kind
	av.Dispatched = true
	av.DispatchAt = now
	av.TargetVersion = version
	av.TargetValue = value
	av.Result = model.ResultPending
	av.FailReason = ""
	av.AckedAt = ""
	if err := s.st.UpsertActivityVehicle(ctx, &av); err != nil {
		return 0, err
	}
	pv := &model.PublishedVersion{
		VehicleID: vehicleID, Version: version, Interval: value,
		Kind: kind, ActivityID: act.ID, CreatedAt: now, CreatedBy: actor.Username,
	}
	if err := s.st.InsertConfigVersion(ctx, pv); err != nil {
		return 0, err
	}
	if err := s.st.UpdateVehicleState(ctx, vehicleID, map[string]any{
		"desired_version": version, "desired_value": value,
		"result": model.ResultPending, "activity_id": act.ID,
	}); err != nil {
		return 0, err
	}
	s.logEvent(ctx, actor, "config.publish",
		fmt.Sprintf("%s@v%d", vehicleID, version), true,
		fmt.Sprintf("下发 %s 版本：目标 %d 秒（下发≠生效）", kindLabel(kind), value), act.ID, vehicleID)
	return version, nil
}

func kindLabel(kind string) string {
	switch kind {
	case model.KindPilot:
		return "试点"
	case model.KindPromote:
		return "推广"
	case model.KindRecover:
		return "恢复"
	}
	return kind
}

// RefreshTimeouts 对外暴露的超时刷新（定时器调用）。
func (s *Service) RefreshTimeouts(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshTimeoutsLocked(ctx)
}

// refreshTimeoutsLocked 扫描待回执的下发记录，超过阈值未获有效回执则标记超时并保留历史（R5）。
// 试点阶段任一车超时 → 活动暂停（R2）。
func (s *Service) refreshTimeoutsLocked(ctx context.Context) error {
	acts, err := s.st.Activities(ctx)
	if err != nil {
		return err
	}
	nowT := s.now()
	for _, act := range acts {
		if !isOpenForTimeout(act.Status) {
			continue
		}
		avs, err := s.st.ActivityVehicles(ctx, act.ID)
		if err != nil {
			return err
		}
		paused := false
		for _, av := range avs {
			if !av.Dispatched || av.Result != model.ResultPending {
				continue
			}
			dispatchT, err := time.Parse(time.RFC3339, av.DispatchAt)
			if err != nil || nowT.Sub(dispatchT) < s.Timeout {
				continue
			}
			if err := s.st.UpdateActivityVehicle(ctx, act.ID, av.VehicleID, map[string]any{
				"result": model.ResultTimeout,
			}); err != nil {
				return err
			}
			if err := s.st.UpdateVehicleState(ctx, av.VehicleID, map[string]any{
				"result": model.ResultTimeout,
			}); err != nil {
				return err
			}
			if err := s.st.InsertTimeoutEvent(ctx, &model.TimeoutEvent{
				ActivityID: act.ID, VehicleID: av.VehicleID,
				Version: av.TargetVersion, At: s.nowStr(),
			}); err != nil {
				return err
			}
			s.logSystemEvent(ctx, "receipt.timeout",
				fmt.Sprintf("%s@v%d", av.VehicleID, av.TargetVersion),
				fmt.Sprintf("发布 %d 秒内未获有效回执，结果显示超时（历史保留）",
					int(s.Timeout.Seconds())),
				act.ID, av.VehicleID)
			if act.Status == model.ActPiloting && isPilot(act, av.VehicleID) {
				paused = true
			}
		}
		if paused {
			if err := s.st.UpdateActivity(ctx, act.ID, map[string]any{
				"status": model.ActPaused, "updated_at": s.nowStr(),
			}); err != nil {
				return err
			}
			s.logSystemEvent(ctx, "activity.paused", act.ID,
				"试点车辆失败或超时，活动暂停推进（推广门禁生效）", act.ID, "")
		}
	}
	return nil
}

func isOpenForTimeout(status string) bool {
	switch status {
	case model.ActPiloting, model.ActPromotionReady, model.ActPromoting, model.ActPaused:
		return true
	}
	return false
}

func isPilot(act model.Activity, vehicleID string) bool {
	for _, id := range act.PilotVehicleIDs {
		if id == vehicleID {
			return true
		}
	}
	return false
}

// contains 判断 vehicleID 是否在列表中。
func contains(list []string, vehicleID string) bool {
	for _, id := range list {
		if id == vehicleID {
			return true
		}
	}
	return false
}

// getVehicleStatesMap 读取全部车辆状态并按车辆编号建索引。
func (s *Service) getVehicleStatesMap(ctx context.Context) (map[string]model.VehicleState, error) {
	states, err := s.st.VehicleStates(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]model.VehicleState, len(states))
	for _, v := range states {
		m[v.VehicleID] = v
	}
	return m, nil
}
