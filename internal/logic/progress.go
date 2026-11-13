package logic

import (
	"context"
	"fmt"
	"strings"

	"fleet/internal/model"
)

// Progress 逐车核查视图：每车发布前值、期望值、生效值、结果与原因（R7 / V19）。
func (s *Service) Progress(ctx context.Context, u model.User, id string) (*model.ActivityProgress, error) {
	if err := s.requireRole(ctx, u, "查看逐车进度", model.RoleApprover, model.RoleOperator, model.RoleApplicant); err != nil {
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
	app, err := s.mustGetApplication(ctx, act.ApplicationID)
	if err != nil {
		return nil, err
	}
	avs, err := s.st.ActivityVehicles(ctx, id)
	if err != nil {
		return nil, err
	}
	states, err := s.getVehicleStatesMap(ctx)
	if err != nil {
		return nil, err
	}
	avByVehicle := make(map[string]model.ActivityVehicle, len(avs))
	for _, av := range avs {
		avByVehicle[av.VehicleID] = av
	}
	progress := &model.ActivityProgress{
		Activity: *act, Application: *app, TimeoutSeconds: int(s.Timeout.Seconds()),
	}
	var (
		unconfirmedComplete []string
		unconfirmedRestore  []string
	)
	for _, vid := range act.TargetVehicleIDs {
		av, ok := avByVehicle[vid]
		if !ok {
			continue
		}
		state := states[vid]
		published, err := s.st.ConfigVersionsOfVehicle(ctx, vid)
		if err != nil {
			return nil, err
		}
		timeouts, err := s.st.TimeoutEventsOfVehicle(ctx, vid)
		if err != nil {
			return nil, err
		}
		dto := make([]model.TimeoutEventDTO, 0, len(timeouts))
		for _, te := range timeouts {
			dto = append(dto, model.TimeoutEventDTO{
				Version: te.Version, At: te.At, ResolvedByLateSuccess: te.ResolvedByLateSuccess,
			})
		}
		progress.Vehicles = append(progress.Vehicles, model.VehicleProgress{
			VehicleID: vid, InActivity: true, Phase: av.Phase,
			Dispatched: av.Dispatched, DispatchAt: av.DispatchAt,
			BaseValue: av.BaseValue, BaseVersion: av.BaseVersion,
			ExpectedVersion: state.DesiredVersion, ExpectedValue: state.DesiredValue,
			EffectiveVersion: state.EffectiveVersion, EffectiveValue: state.EffectiveValue,
			Result: av.Result, FailReason: av.FailReason, AckedAt: av.AckedAt,
			NeedsRecovery:  av.NeedsRecovery,
			TimeoutHistory: dto, PublishedVersions: published,
		})
		if av.Dispatched {
			progress.PublishedVehicleIDs = append(progress.PublishedVehicleIDs, vid)
		} else {
			progress.UnpublishedVehicleIDs = append(progress.UnpublishedVehicleIDs, vid)
		}
		if av.NeedsRecovery {
			progress.NeedsRecoveryVehicleIDs = append(progress.NeedsRecoveryVehicleIDs, vid)
			if av.Phase == model.KindRecover && av.Result == model.ResultSuccess {
				progress.RestoredCount++
			} else {
				unconfirmedRestore = append(unconfirmedRestore, vid)
			}
		}
		switch av.Result {
		case model.ResultSuccess:
			progress.SuccessCount++
		case model.ResultPending:
			progress.PendingVehicleIDs = append(progress.PendingVehicleIDs, vid)
		case model.ResultFailure, model.ResultTimeout:
			progress.FailedVehicleIDs = append(progress.FailedVehicleIDs, vid)
		}
		if av.Result != model.ResultSuccess {
			unconfirmedComplete = append(unconfirmedComplete, fmt.Sprintf("%s(%s)", vid, resultLabel(av.Result)))
		}
	}
	progress.NeedsRecoveryCount = len(progress.NeedsRecoveryVehicleIDs)
	progress.UnconfirmedRestoreIDs = unconfirmedRestore
	progress.AllConfirmed = len(unconfirmedComplete) == 0 && len(act.TargetVehicleIDs) > 0
	progress.AllRestored = progress.NeedsRecoveryCount > 0 && len(unconfirmedRestore) == 0
	progress.AllRecoveredShown = act.Status == model.ActRecovered && progress.AllRestored

	blocker := ""
	for _, vid := range act.PilotVehicleIDs {
		av := avByVehicle[vid]
		if av.Result != model.ResultSuccess {
			blocker = fmt.Sprintf("试点车 %s %s", vid, resultLabel(av.Result))
			break
		}
	}
	switch act.Status {
	case model.ActPromotionReady:
		if blocker == "" {
			progress.CanPromote = true
		} else {
			progress.PromoteBlockReason = blocker
		}
	case model.ActPiloting:
		if blocker == "" {
			progress.PromoteBlockReason = "试点结果尚未全部回执，不能推广"
		} else {
			progress.PromoteBlockReason = "试点未全部成功（" + blocker + "），不能推广"
		}
	case model.ActPaused:
		progress.PromoteBlockReason = "试点失败或超时，活动已暂停；须停止活动或重试试点，禁止绕过检查直接推广"
	case model.ActPromoting:
		progress.PromoteBlockReason = "已进入推广阶段"
	case model.ActStopped:
		progress.PromoteBlockReason = "活动已停止"
	case model.ActRecovering:
		progress.PromoteBlockReason = "活动已进入恢复阶段"
	case model.ActRecovered:
		progress.PromoteBlockReason = "活动已恢复完成"
	case model.ActCompleted:
		progress.PromoteBlockReason = "活动已完成"
	}

	switch act.Status {
	case model.ActPiloting, model.ActPromotionReady, model.ActPromoting:
		if progress.AllConfirmed {
			progress.CanComplete = true
		} else {
			progress.CompleteBlockReason = "仍有未确认车辆：" + strings.Join(unconfirmedComplete, "、")
		}
	default:
		progress.CompleteBlockReason = fmt.Sprintf("活动当前状态 %s 不可记为完成", act.Status)
	}

	progress.RestoreApproved = act.RestoreApprovedAt != ""
	switch {
	case act.Status == model.ActStopped && !progress.RestoreApproved:
		progress.RestoreBlockReason = "活动已停止，等待审批负责人批准恢复"
	case act.Status == model.ActStopped && progress.NeedsRecoveryCount == 0:
		progress.RestoreBlockReason = "该活动从未下发配置，无需恢复"
	case act.Status == model.ActStopped:
		progress.CanPublishRestore = true
	case act.Status == model.ActRecovering:
		progress.RestoreBlockReason = fmt.Sprintf("恢复中：已恢复 %d / 需恢复 %d",
			progress.RestoredCount, progress.NeedsRecoveryCount)
	case act.Status == model.ActRecovered:
		progress.RestoreBlockReason = "所有需恢复车辆均已确认恢复"
	default:
		progress.RestoreBlockReason = "仅已停止的活动可批准/下发恢复"
	}
	return progress, nil
}
