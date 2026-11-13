package logic

import (
	"context"
	"fmt"
	"strings"

	"fleet/internal/model"
)

// CreateActivityInput 创建活动入参：从已批准申请发起试点（2 台）。
type CreateActivityInput struct {
	ApplicationID   string   `json:"application_id"`
	PilotVehicleIDs []string `json:"pilot_vehicle_ids"`
}

// CreateActivity 运营基于已批准申请创建发布活动并下发试点（R2）。
// 门禁：同车队单一未结束活动；试点固定 2 台且属于目标车辆。
func (s *Service) CreateActivity(ctx context.Context, u model.User, in CreateActivityInput) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "创建发布活动", model.RoleOperator); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.mustGetApplication(ctx, in.ApplicationID)
	if err != nil {
		return nil, err
	}
	if app.Status != model.AppApproved {
		err := model.ErrConflict("APP_NOT_APPROVED",
			"申请 %s 尚未批准（当前状态 %s），不能创建发布活动", app.ID, app.Status)
		s.logEvent(ctx, u, "activity.create", in.ApplicationID, false, err.Error(), "", "")
		return nil, err
	}
	if open, ok, err := s.st.OpenActivity(ctx, model.FleetID); err != nil {
		return nil, err
	} else if ok {
		err := model.ErrConflict("ACTIVITY_EXISTS",
			"车队 %s 已存在未结束活动 %s（状态 %s），同一车队同时只允许一个未结束发布活动",
			model.FleetID, open.ID, open.Status)
		s.logEvent(ctx, u, "activity.create", in.ApplicationID, false, err.Error(), open.ID, "")
		return nil, err
	}
	if existed, ok, err := s.st.ActivityByApplication(ctx, app.ID); err != nil {
		return nil, err
	} else if ok {
		err := model.ErrConflict("ACTIVITY_EXISTS",
			"申请 %s 已创建过发布活动 %s", app.ID, existed.ID)
		s.logEvent(ctx, u, "activity.create", app.ID, false, err.Error(), existed.ID, "")
		return nil, err
	}
	pilots := dedupe(in.PilotVehicleIDs)
	if len(pilots) != model.PilotVehicleCount {
		err := model.ErrBadRequest("INVALID_PILOT",
			"试点车辆必须选择 %d 台（当前 %d 台）", model.PilotVehicleCount, len(pilots))
		s.logEvent(ctx, u, "activity.create", app.ID, false, err.Error(), "", "")
		return nil, err
	}
	for _, p := range pilots {
		if !contains(app.VehicleIDs, p) {
			err := model.ErrBadRequest("INVALID_PILOT",
				"试点车辆 %s 不在申请目标车辆范围内", p)
			s.logEvent(ctx, u, "activity.create", app.ID, false, err.Error(), "", "")
			return nil, err
		}
	}
	id, err := s.nextID(ctx, "ACT", "activity")
	if err != nil {
		return nil, err
	}
	now := s.nowStr()
	act := &model.Activity{
		ID: id, ApplicationID: app.ID, FleetID: model.FleetID,
		Status: model.ActPiloting, Interval: app.Interval,
		PilotVehicleIDs: pilots, TargetVehicleIDs: app.VehicleIDs,
		CreatedBy: u.Username, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.InsertActivity(ctx, act); err != nil {
		return nil, err
	}
	// 快照发布前值（活动前该车生效配置，恢复目标）
	states, err := s.getVehicleStatesMap(ctx)
	if err != nil {
		return nil, err
	}
	for _, vid := range act.TargetVehicleIDs {
		st := states[vid]
		if err := s.st.UpsertActivityVehicle(ctx, &model.ActivityVehicle{
			ActivityID: id, VehicleID: vid,
			BaseValue: st.EffectiveValue, BaseVersion: st.EffectiveVersion,
			Result: model.ResultNotPublished,
		}); err != nil {
			return nil, err
		}
	}
	for _, p := range pilots {
		if _, err := s.publishLocked(ctx, act, p, model.KindPilot, act.Interval, u); err != nil {
			return nil, err
		}
	}
	s.logEventWithDetail(ctx, u, "activity.create", id, true,
		fmt.Sprintf("创建发布活动并下发试点：%s（目标 %d 秒）", strings.Join(pilots, "、"), act.Interval),
		fmt.Sprintf(`{"pilot":%q,"interval":%d}`, strings.Join(pilots, ","), act.Interval), id, "")
	return s.mustGetActivity(ctx, id)
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, x := range in {
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

// Promote 运营确认推广剩余车辆（R2 门禁：两台试点均取得有效成功回执）。
func (s *Service) Promote(ctx context.Context, u model.User, id string) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "确认推广", model.RoleOperator); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	act, err := s.mustGetActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	reject := func(format string, args ...any) error {
		err := model.ErrConflict("PROMOTE_BLOCKED", format, args...)
		s.logEvent(ctx, u, "activity.promote", id, false, err.Error(), id, "")
		return err
	}
	if act.Status != model.ActPromotionReady {
		avs, err := s.st.ActivityVehicles(ctx, id)
		if err != nil {
			return nil, err
		}
		detail := pilotDetail(avs, act)
		return nil, reject("试点未全部取得有效成功回执，禁止推广（%s）", detail)
	}
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	remain := make([]string, 0, 3)
	for _, av := range avs {
		if !av.Dispatched && !contains(act.PilotVehicleIDs, av.VehicleID) {
			remain = append(remain, av.VehicleID)
		}
	}
	for _, vid := range remain {
		if _, err := s.publishLocked(ctx, act, vid, model.KindPromote, act.Interval, u); err != nil {
			return nil, err
		}
	}
	if err := s.st.UpdateActivity(ctx, id, map[string]any{
		"status": model.ActPromoting, "updated_at": s.nowStr(),
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "activity.promote", id, true,
		fmt.Sprintf("确认推广剩余车辆：%s（目标 %d 秒）", strings.Join(remain, "、"), act.Interval), id, "")
	return s.mustGetActivity(ctx, id)
}

func pilotDetail(avs []model.ActivityVehicle, act *model.Activity) string {
	parts := make([]string, 0, len(act.PilotVehicleIDs))
	for _, vid := range act.PilotVehicleIDs {
		for _, av := range avs {
			if av.VehicleID == vid {
				parts = append(parts, fmt.Sprintf("%s=%s", vid, resultLabel(av.Result)))
				break
			}
		}
	}
	return strings.Join(parts, " ")
}

func resultLabel(r string) string {
	switch r {
	case model.ResultPending:
		return "已下发待回执"
	case model.ResultSuccess:
		return "成功"
	case model.ResultFailure:
		return "失败"
	case model.ResultTimeout:
		return "超时"
	}
	return r
}

// StopActivity 停止活动（仅阻止未发布部分继续下发；已发布与生效记录全部保留，R6）。
// 允许审批负责人（决定停止）与发布运营（停止未发送部分）。
func (s *Service) StopActivity(ctx context.Context, u model.User, id, reason string) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "停止活动", model.RoleApprover, model.RoleOperator); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	act, err := s.mustGetActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	switch act.Status {
	case model.ActPiloting, model.ActPromotionReady, model.ActPromoting, model.ActPaused:
	default:
		err := model.ErrConflict("INVALID_STATUS",
			"活动 %s 当前状态 %s 不可停止（停止仅针对未结束活动）", id, act.Status)
		s.logEvent(ctx, u, "activity.stop", id, false, err.Error(), id, "")
		return nil, err
	}
	now := s.nowStr()
	if err := s.st.UpdateActivity(ctx, id, map[string]any{
		"status": model.ActStopped, "stopped_at": now, "stopped_by": u.Username,
		"stop_reason": trimSpace(reason), "updated_at": now,
	}); err != nil {
		return nil, err
	}
	// 标记需恢复车辆 = 曾发布过配置的车辆（恢复仅为这些车创建更高版本）
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	published := make([]string, 0, len(avs))
	for _, av := range avs {
		if av.Dispatched {
			published = append(published, av.VehicleID)
			if err := s.st.UpdateActivityVehicle(ctx, id, av.VehicleID, map[string]any{
				"needs_recovery": 1,
			}); err != nil {
				return nil, err
			}
		}
	}
	note := "停止活动：仅阻止未发布部分继续下发；已发布与实际生效记录保留（停止≠撤销车端配置）"
	if len(published) > 0 {
		note += fmt.Sprintf("；曾下发车辆 %s 后续可恢复", strings.Join(published, "、"))
	}
	s.logEvent(ctx, u, "activity.stop", id, true, note, id, "")
	return s.mustGetActivity(ctx, id)
}

// RetryPilot 试点失败/超时暂停后重试试点车辆（假设 A3：仍受 60s 超时约束）。
func (s *Service) RetryPilot(ctx context.Context, u model.User, id string) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "重试试点", model.RoleOperator); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	act, err := s.mustGetActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	if act.Status != model.ActPaused {
		err := model.ErrConflict("INVALID_STATUS",
			"仅暂停状态可重试试点（当前状态 %s）", act.Status)
		s.logEvent(ctx, u, "activity.retry_pilot", id, false, err.Error(), id, "")
		return nil, err
	}
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	retried := make([]string, 0, 2)
	for _, av := range avs {
		if !contains(act.PilotVehicleIDs, av.VehicleID) {
			continue
		}
		if av.Result == model.ResultFailure || av.Result == model.ResultTimeout {
			retried = append(retried, av.VehicleID)
		}
	}
	if len(retried) == 0 {
		err := model.ErrConflict("RETRY_NOT_ALLOWED", "没有失败或超时的试点车辆可重试")
		s.logEvent(ctx, u, "activity.retry_pilot", id, false, err.Error(), id, "")
		return nil, err
	}
	for _, vid := range retried {
		if _, err := s.publishLocked(ctx, act, vid, model.KindPilot, act.Interval, u); err != nil {
			return nil, err
		}
	}
	s.logEvent(ctx, u, "activity.retry_pilot", id, true,
		fmt.Sprintf("重试试点车辆：%s（目标 %d 秒）", strings.Join(retried, "、"), act.Interval), id, "")
	return s.mustGetActivity(ctx, id)
}

// ApproveRestore 负责人批准恢复（R6：停止后才能批准；恢复须新一轮更高版本下发）。
func (s *Service) ApproveRestore(ctx context.Context, u model.User, id string) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "批准恢复", model.RoleApprover); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	act, err := s.mustGetActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	if act.Status != model.ActStopped {
		err := model.ErrConflict("INVALID_STATUS",
			"仅已停止的活动可批准恢复（当前状态 %s）", act.Status)
		s.logEvent(ctx, u, "activity.restore.approve", id, false, err.Error(), id, "")
		return nil, err
	}
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	needs := 0
	for _, av := range avs {
		if av.Dispatched {
			needs++
		}
	}
	if needs == 0 {
		err := model.ErrConflict("RESTORE_NOT_NEEDED",
			"该活动从未向任何车辆下发配置，无需恢复")
		s.logEvent(ctx, u, "activity.restore.approve", id, false, err.Error(), id, "")
		return nil, err
	}
	now := s.nowStr()
	if err := s.st.UpdateActivity(ctx, id, map[string]any{
		"restore_approved_at": now, "restore_approved_by": u.Username, "updated_at": now,
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "activity.restore.approve", id, true,
		fmt.Sprintf("批准恢复：%d 台曾下发车辆将创建更高版本，目标为各车活动前设置", needs), id, "")
	return s.mustGetActivity(ctx, id)
}

// PublishRestore 运营为曾发布过配置的车辆创建恢复版本并下发（R6：更高版本、目标=活动前值）。
func (s *Service) PublishRestore(ctx context.Context, u model.User, id string) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "下发恢复版本", model.RoleOperator); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	act, err := s.mustGetActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	if act.Status != model.ActStopped || act.RestoreApprovedAt == "" {
		err := model.ErrConflict("INVALID_STATUS",
			"活动 %s 未停止或负责人未批准恢复，不能下发恢复版本（当前状态 %s）", id, act.Status)
		s.logEvent(ctx, u, "activity.restore.publish", id, false, err.Error(), id, "")
		return nil, err
	}
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	vehicles, err := s.st.Vehicles(ctx)
	if err != nil {
		return nil, err
	}
	baseline := map[string]int{}
	for _, v := range vehicles {
		baseline[v.ID] = v.BaselineValue
	}
	restored := make([]string, 0, len(avs))
	for _, av := range avs {
		if !av.Dispatched {
			continue
		}
		// 恢复目标=该车的活动前设置（车辆档案基线 60 秒），通过更高版本另一次下发实现
		if _, err := s.publishLocked(ctx, act, av.VehicleID, model.KindRecover, baseline[av.VehicleID], u); err != nil {
			return nil, err
		}
		if err := s.st.UpdateActivityVehicle(ctx, id, av.VehicleID, map[string]any{
			"needs_recovery": 1,
		}); err != nil {
			return nil, err
		}
		restored = append(restored, av.VehicleID)
	}
	if err := s.st.UpdateActivity(ctx, id, map[string]any{
		"status": model.ActRecovering, "updated_at": s.nowStr(),
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "activity.restore.publish", id, true,
		fmt.Sprintf("下发恢复版本：%s（目标=各车活动前值）", strings.Join(restored, "、")), id, "")
	return s.mustGetActivity(ctx, id)
}

// CompleteActivity 负责人将活动记为完成（R7 门禁：全部目标车辆确认成功）。
func (s *Service) CompleteActivity(ctx context.Context, u model.User, id string) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "记活动完成", model.RoleApprover); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	act, err := s.mustGetActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	switch act.Status {
	case model.ActPiloting, model.ActPromotionReady, model.ActPromoting:
	default:
		err := model.ErrConflict("INVALID_STATUS",
			"活动 %s 当前状态 %s 不可记为完成", id, act.Status)
		s.logEvent(ctx, u, "activity.complete", id, false, err.Error(), id, "")
		return nil, err
	}
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	var unconfirmed []string
	for _, av := range avs {
		if av.Result != model.ResultSuccess {
			unconfirmed = append(unconfirmed, fmt.Sprintf("%s(%s)", av.VehicleID, resultLabel(av.Result)))
		}
	}
	if len(unconfirmed) > 0 {
		err := model.ErrConflict("COMPLETE_BLOCKED",
			"仍有未确认成功的车辆：%s；全部目标车辆确认成功后才可记为完成",
			strings.Join(unconfirmed, "、"))
		s.logEvent(ctx, u, "activity.complete", id, false, err.Error(), id, "")
		return nil, err
	}
	now := s.nowStr()
	if err := s.st.UpdateActivity(ctx, id, map[string]any{
		"status": model.ActCompleted, "completed_at": now, "completed_by": u.Username,
		"updated_at": now,
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "activity.complete", id, true, "全部目标车辆确认成功，活动记为完成", id, "")
	return s.mustGetActivity(ctx, id)
}

// CompleteRestore 负责人记恢复完成（R7 门禁：所有需恢复车辆均确认恢复）。
func (s *Service) CompleteRestore(ctx context.Context, u model.User, id string) (*model.Activity, error) {
	if err := s.requireRole(ctx, u, "记恢复完成", model.RoleApprover); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	act, err := s.mustGetActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	if act.Status != model.ActRecovering {
		err := model.ErrConflict("INVALID_STATUS",
			"活动 %s 当前状态 %s 不可记为恢复完成（须先下发恢复版本）", id, act.Status)
		s.logEvent(ctx, u, "activity.restore.complete", id, false, err.Error(), id, "")
		return nil, err
	}
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	var unconfirmed []string
	for _, av := range avs {
		if !av.NeedsRecovery {
			continue
		}
		if !(av.Phase == model.KindRecover && av.Result == model.ResultSuccess) {
			unconfirmed = append(unconfirmed, fmt.Sprintf("%s(%s)", av.VehicleID, resultLabel(av.Result)))
		}
	}
	if len(unconfirmed) > 0 {
		err := model.ErrConflict("RESTORE_BLOCKED",
			"仍有未确认恢复的车辆：%s；全部确认后才可记为恢复完成，禁止显示「全部恢复」",
			strings.Join(unconfirmed, "、"))
		s.logEvent(ctx, u, "activity.restore.complete", id, false, err.Error(), id, "")
		return nil, err
	}
	now := s.nowStr()
	if err := s.st.UpdateActivity(ctx, id, map[string]any{
		"status": model.ActRecovered, "recovered_at": now, "updated_at": now,
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "activity.restore.complete", id, true,
		"所有需恢复车辆均确认恢复，记为恢复完成", id, "")
	return s.mustGetActivity(ctx, id)
}

// GetActivity 查询活动。
func (s *Service) GetActivity(ctx context.Context, u model.User, id string) (*model.Activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mustGetActivity(ctx, id)
}

// ListActivities 活动列表。
func (s *Service) ListActivities(ctx context.Context, u model.User) ([]model.Activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Activities(ctx)
}
