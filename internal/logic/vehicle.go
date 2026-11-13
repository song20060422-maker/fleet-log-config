package logic

import (
	"context"
	"fmt"

	"fleet/internal/model"
)

// PullConfig 模拟车辆认证后拉取面向自己的最新已发布版本（R3）。
// 未试点车 / 未被下发的车拉取不到活动配置（R2、V4），拒绝原因记录在拉取留痕中。
func (s *Service) PullConfig(ctx context.Context, u model.User) (*model.PullResult, error) {
	if err := s.requireVehicle(ctx, u, "拉取配置"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	vid := u.VehicleID
	latest, ok, err := s.st.LatestConfigVersion(ctx, vid)
	if err != nil {
		return nil, err
	}
	if !ok {
		reason := "该车尚未被下发任何活动配置（未试点车不得提前获得活动配置）"
		if err := s.st.InsertConfigPull(ctx, &model.ConfigPull{
			VehicleID: vid, At: s.nowStr(), Granted: false, RejectReason: reason,
		}); err != nil {
			return nil, err
		}
		s.logEvent(ctx, u, "config.pull", vid, false, reason, "", vid)
		return &model.PullResult{Granted: false, RejectReason: reason, VehicleID: vid}, nil
	}
	if err := s.st.MarkDelivered(ctx, vid, latest.Version); err != nil {
		return nil, err
	}
	if err := s.st.InsertConfigPull(ctx, &model.ConfigPull{
		VehicleID: vid, Version: latest.Version, At: s.nowStr(), Granted: true,
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "config.pull", fmt.Sprintf("%s@v%d", vid, latest.Version), true,
		fmt.Sprintf("拉取最新已发布版本（%s，目标 %d 秒）", kindLabel(latest.Kind), latest.Interval),
		latest.ActivityID, vid)
	return &model.PullResult{
		Granted: true, VehicleID: vid, Version: latest.Version, Interval: latest.Interval,
		Kind: latest.Kind, ActivityID: latest.ActivityID, PublishedAt: latest.CreatedAt,
	}, nil
}

// requireVehicle 校验车辆身份（token 绑定车辆，不接受请求参数里的裸 ID）。
func (s *Service) requireVehicle(ctx context.Context, u model.User, action string) error {
	if !u.Is(model.RoleVehicle) || u.VehicleID == "" {
		err := model.ErrForbidden("FORBIDDEN_ROLE", "仅预置模拟车辆账号可%s", action)
		s.logEvent(ctx, u, "vehicle."+action, "", false, err.Error(), "", "")
		return err
	}
	return nil
}

// VehicleState 模拟车辆查询自己的期望值/生效值/失败原因（R3/R7）。
func (s *Service) VehicleState(ctx context.Context, u model.User) (*model.VehicleState, error) {
	if err := s.requireVehicle(ctx, u, "查询车辆状态"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	st, ok, err := s.st.VehicleState(ctx, u.VehicleID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, model.ErrNotFound("车辆 %s 状态不存在", u.VehicleID)
	}
	return &st, nil
}

// ReportReceipt 车辆上报回执（R4/R5：鉴权 → 车辆匹配 → 版本匹配，任一失败即拒绝留痕）。
func (s *Service) ReportReceipt(ctx context.Context, u model.User, in model.ReceiptInput) (*model.ReceiptResult, error) {
	if err := s.requireVehicle(ctx, u, "上报回执"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	vid := u.VehicleID
	outcome := "failure"
	if in.Success {
		outcome = "success"
	}
	// reject：拒绝留痕并返回业务错误
	reject := func(code string, status int, format string, args ...any) (*model.ReceiptResult, error) {
		reason := fmt.Sprintf(format, args...)
		if _, err := s.st.InsertReceipt(ctx, &model.Receipt{
			VehicleID: vid, Version: in.Version, Outcome: outcome, Message: in.Message,
			Accepted: false, Recorded: false, RejectReason: reason, At: s.nowStr(),
		}); err != nil {
			return nil, err
		}
		s.logEvent(ctx, u, "receipt."+outcome, fmt.Sprintf("%s@v%d", vid, in.Version),
			false, reason, "", vid)
		bizErr := model.NewErr(status, code, "%s", reason)
		return nil, bizErr
	}
	// 跨车：token 身份与回执声称的车辆不一致（R8，身份以鉴权为准）
	if in.VehicleID != "" && in.VehicleID != vid {
		return reject("CROSS_VEHICLE", 403,
			"跨车回执被拒绝：凭证身份 %s 与回执车辆 %s 不一致", vid, in.VehicleID)
	}
	if in.Version <= 0 {
		return reject("INVALID_VERSION", 400, "回执版本号非法（须为正整数）")
	}
	owner, ok, err := s.st.ConfigVersionOwner(ctx, in.Version)
	if err != nil {
		return nil, err
	}
	if !ok {
		return reject("UNKNOWN_VERSION", 409,
			"未知版本 %d 被拒绝：该车从未被发布过该版本", in.Version)
	}
	if owner != vid {
		return reject("CROSS_VEHICLE", 403,
			"跨车回执被拒绝：版本 %d 面向车辆 %s 发布，与凭证身份 %s 不一致", in.Version, owner, vid)
	}
	state, _, err := s.st.VehicleState(ctx, vid)
	if err != nil {
		return nil, err
	}
	if in.Version < state.DesiredVersion {
		return reject("STALE_VERSION", 409,
			"旧版本迟到回执被拒绝：仅留痕，不覆盖新生效值（生效值保持版本 %d）", state.EffectiveVersion)
	}
	if in.Version > state.DesiredVersion {
		return reject("UNKNOWN_VERSION", 409,
			"未知版本 %d 被拒绝：该车从未被发布过该版本", in.Version)
	}
	// 当前期望版本
	latest, _, err := s.st.LatestConfigVersion(ctx, vid)
	if err != nil {
		return nil, err
	}
	act, actOK, err := s.st.Activity(ctx, latest.ActivityID)
	if err != nil {
		return nil, err
	}
	activityID := ""
	if actOK {
		activityID = act.ID
	}
	// 已停止/已结束活动：迟到回执仅留痕，不改变生效值，也不重启活动（R5、假设 A5）
	if actOK && activityClosed(act.Status) {
		return reject("ACTIVITY_CLOSED", 409,
			"活动 %s 已结束（%s）：迟到回执仅留痕，不改变生效值与活动状态（停止≠撤销）",
			act.ID, act.Status)
	}
	if in.Success {
		return s.applyReceiptSuccess(ctx, u, vid, in, state, latest, activityID)
	}
	return s.applyReceiptFailure(ctx, u, vid, in, latest, activityID)
}

func activityClosed(status string) bool {
	switch status {
	case model.ActStopped, model.ActRecovered, model.ActCompleted:
		return true
	}
	return false
}

// applyReceiptSuccess 当前版本成功回执：更新生效值（唯一写路径），幂等不重复记账（R4）。
func (s *Service) applyReceiptSuccess(ctx context.Context, u model.User, vid string,
	in model.ReceiptInput, state model.VehicleState, latest model.PublishedVersion, activityID string) (*model.ReceiptResult, error) {
	idempotent := state.EffectiveVersion == latest.Version && state.Result == model.ResultSuccess
	recorded := !idempotent
	now := s.nowStr()
	if recorded {
		if err := s.st.UpdateVehicleState(ctx, vid, map[string]any{
			"effective_version": latest.Version, "effective_value": latest.Interval,
			"result": model.ResultSuccess, "fail_reason": "",
		}); err != nil {
			return nil, err
		}
		if err := s.st.UpdateActivityVehicle(ctx, activityID, vid, map[string]any{
			"result": model.ResultSuccess, "fail_reason": "", "acked_at": now,
		}); err != nil {
			return nil, err
		}
		// 超时历史保留；迟到成功仅翻转结果并标记（R5）
		if err := s.st.ResolveTimeoutEvents(ctx, vid, latest.Version); err != nil {
			return nil, err
		}
	} else {
		if err := s.st.UpdateActivityVehicle(ctx, activityID, vid, map[string]any{
			"result": model.ResultSuccess, "fail_reason": "", "acked_at": now,
		}); err != nil {
			return nil, err
		}
	}
	if _, err := s.st.InsertReceipt(ctx, &model.Receipt{
		VehicleID: vid, Version: latest.Version, Outcome: "success", Message: in.Message,
		Accepted: true, Recorded: recorded, ActivityID: activityID, At: s.nowStr(),
	}); err != nil {
		return nil, err
	}
	reason := fmt.Sprintf("当前版本 v%d 成功回执：生效值更新为 %d 秒", latest.Version, latest.Interval)
	if idempotent {
		reason = fmt.Sprintf("重复成功回执（v%d）：幂等留痕，不重复记账", latest.Version)
	}
	s.logEvent(ctx, u, "receipt.success", fmt.Sprintf("%s@v%d", vid, latest.Version),
		true, reason, activityID, vid)
	if recorded && activityID != "" {
		if err := s.advanceActivityOnSuccessLocked(ctx, activityID); err != nil {
			return nil, err
		}
	}
	newState, _, err := s.st.VehicleState(ctx, vid)
	if err != nil {
		return nil, err
	}
	return &model.ReceiptResult{
		Accepted: true, Recorded: recorded, EffectiveValue: newState.EffectiveValue,
		EffectiveVersion: newState.EffectiveVersion, Result: newState.Result,
	}, nil
}

// applyReceiptFailure 失败回执：结果记失败并保留原因，生效值不变（R5，V14）。
func (s *Service) applyReceiptFailure(ctx context.Context, u model.User, vid string,
	in model.ReceiptInput, latest model.PublishedVersion, activityID string) (*model.ReceiptResult, error) {
	reason := trimSpace(in.Message)
	if reason == "" {
		reason = "车端应用失败"
	}
	now := s.nowStr()
	if err := s.st.UpdateVehicleState(ctx, vid, map[string]any{
		"result": model.ResultFailure, "fail_reason": reason,
	}); err != nil {
		return nil, err
	}
	if activityID != "" {
		if err := s.st.UpdateActivityVehicle(ctx, activityID, vid, map[string]any{
			"result": model.ResultFailure, "fail_reason": reason, "acked_at": now,
		}); err != nil {
			return nil, err
		}
		// 试点阶段任一车失败 → 活动暂停（R2）
		act, ok, err := s.st.Activity(ctx, activityID)
		if err != nil {
			return nil, err
		}
		if ok && act.Status == model.ActPiloting && isPilot(act, vid) {
			if err := s.st.UpdateActivity(ctx, activityID, map[string]any{
				"status": model.ActPaused, "updated_at": s.nowStr(),
			}); err != nil {
				return nil, err
			}
			s.logSystemEvent(ctx, "activity.paused", activityID,
				fmt.Sprintf("试点车 %s 应用失败（%s），活动暂停推进", vid, reason), activityID, vid)
		}
	}
	if _, err := s.st.InsertReceipt(ctx, &model.Receipt{
		VehicleID: vid, Version: latest.Version, Outcome: "failure", Message: in.Message,
		Accepted: true, Recorded: true, ActivityID: activityID, At: s.nowStr(),
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "receipt.failure", fmt.Sprintf("%s@v%d", vid, latest.Version),
		true, fmt.Sprintf("失败回执：%s（生效值保持上一个成功版本）", reason), activityID, vid)
	newState, _, err := s.st.VehicleState(ctx, vid)
	if err != nil {
		return nil, err
	}
	return &model.ReceiptResult{
		Accepted: true, Recorded: true, EffectiveValue: newState.EffectiveValue,
		EffectiveVersion: newState.EffectiveVersion, Result: newState.Result,
	}, nil
}

// advanceActivityOnSuccessLocked 成功回执后的活动推进（只推进未结束活动，R5）。
func (s *Service) advanceActivityOnSuccessLocked(ctx context.Context, activityID string) error {
	act, ok, err := s.st.Activity(ctx, activityID)
	if err != nil || !ok {
		return err
	}
	if act.Status != model.ActPiloting && act.Status != model.ActPaused {
		return nil
	}
	avs, err := s.st.ActivityVehicles(ctx, activityID)
	if err != nil {
		return err
	}
	allSuccess := true
	for _, vid := range act.PilotVehicleIDs {
		found := false
		for _, av := range avs {
			if av.VehicleID == vid {
				found = true
				if av.Result != model.ResultSuccess {
					allSuccess = false
				}
				break
			}
		}
		if !found {
			allSuccess = false
		}
	}
	if allSuccess && (act.Status == model.ActPiloting || act.Status == model.ActPaused) {
		if err := s.st.UpdateActivity(ctx, activityID, map[string]any{
			"status": model.ActPromotionReady, "updated_at": s.nowStr(),
		}); err != nil {
			return err
		}
		s.logSystemEvent(ctx, "activity.promotion_ready", activityID,
			"两台试点均取得有效成功回执，活动待推广", activityID, "")
	}
	return nil
}
