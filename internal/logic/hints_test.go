package logic_test

import (
	"context"
	"strings"
	"testing"

	"fleet/internal/logic"
	"fleet/internal/model"
)

// TestProgressStageHints 覆盖各阶段提示文案与恢复门禁展示。
func TestProgressStageHints(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{3, 4, 5} {
		e.ack(t, idx, true, "应用成功")
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, act.ID); err != nil {
		t.Fatal(err)
	}
	p := e.progress(t, act.ID)
	if p.PromoteBlockReason != "活动已完成" {
		t.Fatalf("已完成活动提示不正确: %s", p.PromoteBlockReason)
	}
	if p.CanPromote || p.CanPublishRestore {
		t.Fatal("已完成活动不可再推广或恢复")
	}

	// 停止阶段：等待批准恢复
	act2 := e.mustActivity(t, 45, "VEH-01", "VEH-02")
	if _, err := e.svc.StopActivity(ctx, approver, act2.ID, "停止以核查提示"); err != nil {
		t.Fatal(err)
	}
	p2 := e.progress(t, act2.ID)
	if !strings.Contains(p2.RestoreBlockReason, "等待审批负责人批准恢复") {
		t.Fatalf("停止阶段提示不正确: %s", p2.RestoreBlockReason)
	}
	if p2.CanPublishRestore || p2.RestoreApproved {
		t.Fatal("未经批准不得下发恢复")
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, act2.ID); err != nil {
		t.Fatal(err)
	}
	p2 = e.progress(t, act2.ID)
	if !p2.RestoreApproved || !p2.CanPublishRestore {
		t.Fatalf("批准后应允许下发恢复: %+v", p2)
	}
	if _, err := e.svc.PublishRestore(ctx, operator, act2.ID); err != nil {
		t.Fatal(err)
	}
	p2 = e.progress(t, act2.ID)
	if !strings.Contains(p2.RestoreBlockReason, "恢复中：已恢复 0 / 需恢复 2") {
		t.Fatalf("恢复中提示不正确: %s", p2.RestoreBlockReason)
	}
	e.ack(t, 1, true, "已恢复")
	e.ack(t, 2, true, "已恢复")
	if _, err := e.svc.CompleteRestore(ctx, approver, act2.ID); err != nil {
		t.Fatal(err)
	}
	p2 = e.progress(t, act2.ID)
	if !strings.Contains(p2.RestoreBlockReason, "所有需恢复车辆均已确认恢复") || !p2.AllRecoveredShown {
		t.Fatalf("恢复完成提示不正确: %s shown=%v", p2.RestoreBlockReason, p2.AllRecoveredShown)
	}
}

// TestExportEdges 覆盖导出中拉取记录、多活动下的结果映射与留痕权限。
func TestExportEdges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	// 先制造一条被拒拉取（此时尚无任何车辆被下发版本）
	if res := e.pull(t, 3); res.Granted {
		t.Fatal("未下发车辆拉取应被拒绝")
	}
	_ = e.pull(t, 1)
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{3, 4, 5} {
		e.ack(t, idx, true, "应用成功")
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, act.ID); err != nil {
		t.Fatal(err)
	}
	// 拉取记录：被拒（无版本）与被受理（有版本）两种
	// 第二个活动，试点下发后立即停止 → 存在待确认车辆
	app := e.mustApprovedApp(t, 20)
	act2, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
		ApplicationID: app.ID, PilotVehicleIDs: []string{"VEH-01", "VEH-02"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StopActivity(ctx, approver, act2.ID, "停止以验证导出"); err != nil {
		t.Fatal(err)
	}
	state, err := e.svc.ExportState(ctx, approver)
	if err != nil {
		t.Fatal(err)
	}
	pulls := state["config_pulls"].([]map[string]any)
	if len(pulls) != 2 {
		t.Fatalf("应有 2 条拉取记录，实际 %d", len(pulls))
	}
	for _, p := range pulls {
		if !p["granted"].(bool) && p["version"] != nil {
			t.Fatalf("被拒拉取的版本应为 null: %+v", p)
		}
		if p["granted"].(bool) && p["version"].(int) == 0 {
			t.Fatalf("被受理拉取应带版本号: %+v", p)
		}
	}
	results := map[string]string{}
	for _, v := range state["vehicles"].([]map[string]any) {
		results[v["id"].(string)] = v["result"].(string)
	}
	// 多活动遍历：VEH-01 的期望值属于活动2（停止）→ unconfirmed；其余 completed 活动成功车保持 success
	if results["VEH-01"] != "unconfirmed" || results["VEH-02"] != "unconfirmed" {
		t.Fatalf("停止活动的试点车应为 unconfirmed: %v %v", results["VEH-01"], results["VEH-02"])
	}
	if results["VEH-03"] != model.ResultSuccess {
		t.Fatalf("完成活动的推广车应保持 success: %s", results["VEH-03"])
	}
	// 留痕权限
	if _, err := e.svc.AuditEvents(ctx, vehicleUser(1), 0); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("车辆不得查询留痕: %v", err)
	}
	events, err := e.svc.AuditEvents(ctx, operator, 5)
	if err != nil || len(events) == 0 || len(events) > 5 {
		t.Fatalf("留痕查询失败: %v %d", err, len(events))
	}
}

// TestApplicationInputEdgeCases 覆盖入参校验的收尾分支（去空白、等长不同车辆集）。
func TestApplicationInputEdgeCases(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app, err := e.svc.CreateApplication(ctx, applicant, model.ApplicationInput{
		Reason: "  原因说明\t", VehicleIDs: allVehicles(), Interval: 30,
	})
	if err != nil {
		t.Fatalf("首尾空白应被去除: %v", err)
	}
	if app.Reason != "原因说明" {
		t.Fatalf("原因应去除首尾空白: %q", app.Reason)
	}
	if _, err := e.svc.CreateApplication(ctx, applicant, model.ApplicationInput{
		Reason: "原因", VehicleIDs: []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-09"}, Interval: 30,
	}); errCode(err) != "INVALID_VEHICLES" {
		t.Fatalf("等长但非预置五台应拒绝: %v", err)
	}
}
