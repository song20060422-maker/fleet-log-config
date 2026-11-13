package logic

import (
	"context"
	"fmt"

	"fleet/internal/model"
	"fleet/internal/store"
)

// ExportState 导出业务状态（Skill fleet-log-config-check 输入 state.json，Schema v1）。
// 只读接口：本身也是"逐车核查"能力的组成部分，供测试/验收 Agent 离线核查。
func (s *Service) ExportState(ctx context.Context, u model.User) (map[string]any, error) {
	if err := s.requireRole(ctx, u, "导出业务状态", model.RoleApprover, model.RoleOperator); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshTimeoutsLocked(ctx); err != nil {
		return nil, err
	}
	vehicles, err := s.st.Vehicles(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.getVehicleStatesMap(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := s.st.Applications(ctx)
	if err != nil {
		return nil, err
	}
	acts, err := s.st.Activities(ctx)
	if err != nil {
		return nil, err
	}
	receipts, err := s.st.Receipts(ctx)
	if err != nil {
		return nil, err
	}
	pulls, err := s.st.ConfigPulls(ctx)
	if err != nil {
		return nil, err
	}
	timeouts, err := s.st.TimeoutEvents(ctx)
	if err != nil {
		return nil, err
	}

	vehicleIDs := make([]string, 0, len(vehicles))
	for _, v := range vehicles {
		vehicleIDs = append(vehicleIDs, v.ID)
	}

	// 活动：已发/未发范围、试点版本、需恢复车辆、展示标志
	activityList := make([]map[string]any, 0, len(acts))
	for _, act := range acts {
		avs, err := s.st.ActivityVehicles(ctx, act.ID)
		if err != nil {
			return nil, err
		}
		versions, err := s.st.ConfigVersionsOfActivity(ctx, act.ID)
		if err != nil {
			return nil, err
		}
		published := make([]string, 0, len(avs))
		unpublished := make([]string, 0, len(avs))
		needsRecovery := make([]string, 0, len(avs))
		allRestored := true
		for _, av := range avs {
			if av.Dispatched {
				published = append(published, av.VehicleID)
			} else {
				unpublished = append(unpublished, av.VehicleID)
			}
			if av.NeedsRecovery {
				needsRecovery = append(needsRecovery, av.VehicleID)
				if !(av.Phase == model.KindRecover && av.Result == model.ResultSuccess) {
					allRestored = false
				}
			}
		}
		pilotVersions := make([]int, 0, 2)
		for _, v := range versions {
			if v.Kind == model.KindPilot {
				pilotVersions = append(pilotVersions, v.Version)
			}
		}
		activityList = append(activityList, map[string]any{
			"id": act.ID, "application_id": act.ApplicationID, "fleet_id": act.FleetID,
			"status": act.Status, "pilot_vehicle_ids": act.PilotVehicleIDs,
			"target_vehicle_ids":       act.TargetVehicleIDs,
			"pilot_published_versions": pilotVersions,
			"published_vehicle_ids":    published, "unpublished_vehicle_ids": unpublished,
			"needs_recovery_vehicle_ids": needsRecovery,
			"stopped_at":                 act.StoppedAt,
			"display_flags": map[string]any{
				"all_recovered_shown": act.Status == model.ActRecovered && len(needsRecovery) > 0 && allRestored,
			},
		})
	}

	// 逐车：发布前值 / 期望值 / 生效值 / 结果 / 原因 / 超时历史 / 已发布版本
	vehicleList := make([]map[string]any, 0, len(vehicles))
	for _, v := range vehicles {
		st := states[v.ID]
		published, err := s.st.ConfigVersionsOfVehicle(ctx, v.ID)
		if err != nil {
			return nil, err
		}
		pvList := make([]map[string]any, 0, len(published))
		for _, pv := range published {
			pvList = append(pvList, map[string]any{
				"version": pv.Version, "interval": pv.Interval, "kind": pv.Kind,
				"activity_id": pv.ActivityID, "created_at": pv.CreatedAt,
			})
		}
		timeoutList := make([]map[string]any, 0, 2)
		lateSuccess := false
		for _, te := range timeouts {
			if te.VehicleID != v.ID {
				continue
			}
			timeoutList = append(timeoutList, map[string]any{
				"version": te.Version, "at": te.At,
				"resolved_by_late_success": te.ResolvedByLateSuccess,
			})
			if te.ResolvedByLateSuccess {
				lateSuccess = true
			}
		}
		result := exportResult(st.Result, st, acts)
		var expectedValue, expectedVersion any = nil, nil
		if st.DesiredVersion > 0 {
			expectedValue, expectedVersion = st.DesiredValue, st.DesiredVersion
		}
		vehicleList = append(vehicleList, map[string]any{
			"id": v.ID, "fleet_id": v.FleetID,
			"pre_activity_value": v.BaselineValue,
			"expected_value":     expectedValue, "expected_version": expectedVersion,
			"effective_value": st.EffectiveValue, "effective_version": st.EffectiveVersion,
			"result": result, "fail_reason": st.FailReason,
			"timeout_events": timeoutList, "timeout_history_kept": true,
			"late_success_after_timeout": lateSuccess,
			"published_versions":         pvList,
		})
	}

	appList := make([]map[string]any, 0, len(apps))
	for _, app := range apps {
		var approver any
		if app.Approver != "" {
			approver = app.Approver
		}
		appList = append(appList, map[string]any{
			"id": app.ID, "reason": app.Reason, "vehicle_ids": app.VehicleIDs,
			"interval": app.Interval, "applicant": app.Applicant, "approver": approver,
			"status": app.Status, "param_history": s.ApplicationParamHistory(ctx, app),
		})
	}

	pullList := make([]map[string]any, 0, len(pulls))
	for _, p := range pulls {
		var version any = p.Version
		if p.Version == 0 {
			version = nil
		}
		pullList = append(pullList, map[string]any{
			"vehicle_id": p.VehicleID, "version": version, "at": p.At,
			"granted": p.Granted, "reject_reason": p.RejectReason,
		})
	}

	receiptList := make([]map[string]any, 0, len(receipts))
	for _, r := range receipts {
		receiptList = append(receiptList, map[string]any{
			"id": fmt.Sprintf("R-%d", r.ID), "vehicle_id": r.VehicleID, "version": r.Version,
			"outcome": r.Outcome, "accepted": r.Accepted, "recorded": r.Recorded,
			"reject_reason": r.RejectReason, "activity_id": r.ActivityID, "at": r.At,
		})
	}

	return map[string]any{
		"preset": map[string]any{
			"fleet_id": model.FleetID, "vehicle_ids": vehicleIDs,
			"initial_interval": model.InitialInterval,
			"interval_min":     model.IntervalMin, "interval_max": model.IntervalMax,
			"timeout_seconds": int(s.Timeout.Seconds()),
		},
		"applications": appList,
		"activities":   activityList,
		"vehicles":     vehicleList,
		"config_pulls": pullList,
		"receipts":     receiptList,
	}, nil
}

// exportResult 车辆结果映射为 Skill Schema 取值：
// not_published | published（已下发待回执）| success | failure | timeout | unconfirmed（停止/恢复阶段待确认）。
func exportResult(result string, st model.VehicleState, acts []model.Activity) string {
	if result == model.ResultPending {
		for _, act := range acts {
			if act.ID != st.ActivityID {
				continue
			}
			if act.Status == model.ActStopped || act.Status == model.ActRecovering {
				return "unconfirmed"
			}
		}
		return "published"
	}
	return result
}

// Meta 演示元信息：车队、五台车辆、预置账号与固定口径（前端登录与页面渲染使用）。
func (s *Service) Meta(ctx context.Context) (map[string]any, error) {
	vehicles, err := s.st.Vehicles(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.getVehicleStatesMap(ctx)
	if err != nil {
		return nil, err
	}
	vehicleIDs := make([]string, 0, len(vehicles))
	vehicleList := make([]map[string]any, 0, len(vehicles))
	for _, v := range vehicles {
		vehicleIDs = append(vehicleIDs, v.ID)
		st := states[v.ID]
		vehicleList = append(vehicleList, map[string]any{
			"id": v.ID, "fleet_id": v.FleetID, "online": v.Online,
			"baseline_value":  v.BaselineValue,
			"effective_value": st.EffectiveValue, "effective_version": st.EffectiveVersion,
			"desired_value": st.DesiredValue, "desired_version": st.DesiredVersion,
			"result": st.Result,
		})
	}
	accounts := make([]map[string]any, 0, 8)
	for _, u := range store.PresetUsers() {
		accounts = append(accounts, map[string]any{
			"username": u.Username, "name": u.Name, "role": u.Role, "vehicle_id": u.VehicleID,
		})
	}
	return map[string]any{
		"fleet_id": model.FleetID, "vehicles": vehicleList,
		"preset_vehicle_ids": vehicleIDs,
		"interval_min":       model.IntervalMin, "interval_max": model.IntervalMax,
		"initial_interval": model.InitialInterval,
		"timeout_seconds":  int(s.Timeout.Seconds()),
		"demo_password":    store.PresetPassword,
		"accounts":         accounts,
	}, nil
}

// AuditEvents 留痕事件（越权/拒绝/关键动作）。
func (s *Service) AuditEvents(ctx context.Context, u model.User, limit int) ([]model.Event, error) {
	if err := s.requireRole(ctx, u, "查看留痕", model.RoleApprover, model.RoleOperator); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Events(ctx, limit)
}
