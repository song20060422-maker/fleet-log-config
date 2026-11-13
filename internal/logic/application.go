package logic

import (
	"context"
	"encoding/json"
	"fmt"

	"fleet/internal/model"
)

// CreateApplication 研发申请人创建申请（R1：原因 + 五台目标车辆 + 间隔 10~300）。
func (s *Service) CreateApplication(ctx context.Context, u model.User, in model.ApplicationInput) (*model.Application, error) {
	if err := s.requireRole(ctx, u, "创建申请", model.RoleApplicant); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createApplicationLocked(ctx, u, in)
}

func (s *Service) createApplicationLocked(ctx context.Context, u model.User, in model.ApplicationInput) (*model.Application, error) {
	reason, interval, vehicles, err := s.validateApplicationInput(ctx, in)
	if err != nil {
		s.logEvent(ctx, u, "application.create", "", false, err.Error(), "", "")
		return nil, err
	}
	id, err := s.nextID(ctx, "APP", "application")
	if err != nil {
		return nil, err
	}
	now := s.nowStr()
	app := &model.Application{
		ID: id, FleetID: model.FleetID, Reason: reason, Interval: interval,
		VehicleIDs: vehicles, Applicant: u.Username, Status: model.AppPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.InsertApplication(ctx, app); err != nil {
		return nil, err
	}
	s.logEventWithDetail(ctx, u, "application.create", id, true,
		fmt.Sprintf("提交申请：间隔=%d 车辆=%v", interval, vehicles),
		appParamDetail(interval, reason), "", "")
	return app, nil
}

// ReviseApplication 退回后修改再提交（R1：仅 rejected 状态可修改；重新进入 pending）。
func (s *Service) ReviseApplication(ctx context.Context, u model.User, id string, in model.ApplicationInput) (*model.Application, error) {
	if err := s.requireRole(ctx, u, "修改申请", model.RoleApplicant); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.mustGetApplication(ctx, id)
	if err != nil {
		return nil, err
	}
	if app.Status == model.AppApproved || app.Frozen {
		err := model.ErrConflict("PARAM_FROZEN",
			"申请 %s 已批准，参数冻结不可原地修改；如需变更请新建申请", id)
		s.logEvent(ctx, u, "application.revise", id, false, err.Error(), "", "")
		return nil, err
	}
	if app.Status != model.AppRejected {
		err := model.ErrConflict("INVALID_STATUS",
			"仅被退回的申请可修改再提交（当前状态 %s）", app.Status)
		s.logEvent(ctx, u, "application.revise", id, false, err.Error(), "", "")
		return nil, err
	}
	if app.Applicant != u.Username {
		err := model.ErrForbidden("FORBIDDEN_OWNER", "只能修改本人提交的申请")
		s.logEvent(ctx, u, "application.revise", id, false, err.Error(), "", "")
		return nil, err
	}
	reason, interval, vehicles, err := s.validateApplicationInput(ctx, in)
	if err != nil {
		s.logEvent(ctx, u, "application.revise", id, false, err.Error(), "", "")
		return nil, err
	}
	if err := s.st.UpdateApplication(ctx, id, map[string]any{
		"reason": reason, "interval_seconds": interval, "vehicle_ids": joinIDs(vehicles),
		"status": model.AppPending, "reject_reason": "",
		"updated_at": s.nowStr(),
	}); err != nil {
		return nil, err
	}
	s.logEventWithDetail(ctx, u, "application.revise", id, true,
		fmt.Sprintf("修改后再提交：间隔=%d 车辆=%v", interval, vehicles),
		appParamDetail(interval, reason), "", "")
	return s.mustGetApplication(ctx, id)
}

// ApproveApplication 负责人批准（R1：审批人不能是申请人；批准后参数冻结）。
func (s *Service) ApproveApplication(ctx context.Context, u model.User, id string) (*model.Application, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.mustGetApplication(ctx, id)
	if err != nil {
		return nil, err
	}
	if app.Applicant == u.Username {
		err := model.ErrForbidden("SELF_APPROVAL",
			"审批人不能是申请人（申请人 %s 试图自审批）", app.Applicant)
		s.logEvent(ctx, u, "application.approve", id, false, err.Error(), "", "")
		return nil, err
	}
	if err := s.requireRole(ctx, u, "审批申请", model.RoleApprover); err != nil {
		return nil, err
	}
	if app.Status != model.AppPending {
		err := model.ErrConflict("INVALID_STATUS",
			"仅待审批状态的申请可批准（当前状态 %s）", app.Status)
		s.logEvent(ctx, u, "application.approve", id, false, err.Error(), "", "")
		return nil, err
	}
	now := s.nowStr()
	if err := s.st.UpdateApplication(ctx, id, map[string]any{
		"status": model.AppApproved, "approver": u.Username, "approved_at": now,
		"updated_at": now, "frozen": 1,
	}); err != nil {
		return nil, err
	}
	s.logEventWithDetail(ctx, u, "application.approve", id, true,
		fmt.Sprintf("批准申请：间隔=%d（参数冻结）", app.Interval),
		appParamDetail(app.Interval, app.Reason), "", "")
	return s.mustGetApplication(ctx, id)
}

// RejectApplication 负责人退回（须附原因；退回后申请人可修改再提交）。
func (s *Service) RejectApplication(ctx context.Context, u model.User, id, reason string) (*model.Application, error) {
	if trimSpace(reason) == "" {
		return nil, model.ErrBadRequest("INVALID_REASON", "退回必须填写原因")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.mustGetApplication(ctx, id)
	if err != nil {
		return nil, err
	}
	if app.Applicant == u.Username {
		err := model.ErrForbidden("SELF_APPROVAL",
			"审批人不能是申请人（申请人 %s 试图自审批）", app.Applicant)
		s.logEvent(ctx, u, "application.reject", id, false, err.Error(), "", "")
		return nil, err
	}
	if err := s.requireRole(ctx, u, "退回申请", model.RoleApprover); err != nil {
		return nil, err
	}
	if app.Status != model.AppPending {
		err := model.ErrConflict("INVALID_STATUS",
			"仅待审批状态的申请可退回（当前状态 %s）", app.Status)
		s.logEvent(ctx, u, "application.reject", id, false, err.Error(), "", "")
		return nil, err
	}
	if err := s.st.UpdateApplication(ctx, id, map[string]any{
		"status": model.AppRejected, "approver": u.Username,
		"reject_reason": trimSpace(reason), "updated_at": s.nowStr(),
	}); err != nil {
		return nil, err
	}
	s.logEvent(ctx, u, "application.reject", id, true,
		fmt.Sprintf("退回申请：原因=%s", trimSpace(reason)), "", "")
	return s.mustGetApplication(ctx, id)
}

// GetApplication 查询申请详情。
func (s *Service) GetApplication(ctx context.Context, u model.User, id string) (*model.Application, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mustGetApplication(ctx, id)
}

// ListApplications 申请列表。
func (s *Service) ListApplications(ctx context.Context, u model.User) ([]model.Application, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Applications(ctx)
}

func appParamDetail(interval int, reason string) string {
	b, _ := json.Marshal(map[string]any{"interval": interval, "reason": reason})
	return string(b)
}

// ApplicationParamHistory 导出申请参数历史（Skill C03 检查用）。
// 由留痕事件重建：create/revise 记录 pending 参数，approve 记录批准时参数。
func (s *Service) ApplicationParamHistory(ctx context.Context, app model.Application) []map[string]any {
	events, err := s.st.Events(ctx, 10000)
	if err != nil {
		return nil
	}
	history := make([]map[string]any, 0, 4)
	for _, e := range events {
		if e.Target != app.ID {
			continue
		}
		var status string
		switch e.Action {
		case "application.create", "application.revise":
			status = model.AppPending
		case "application.approve":
			status = model.AppApproved
		default:
			continue
		}
		entry := map[string]any{"at": e.At, "status": status}
		var detail map[string]any
		if json.Unmarshal([]byte(e.Detail), &detail) == nil {
			entry["interval"] = detail["interval"]
			entry["reason"] = detail["reason"]
		}
		history = append(history, entry)
	}
	return history
}
