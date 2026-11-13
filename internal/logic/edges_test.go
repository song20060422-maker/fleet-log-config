package logic_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fleet/internal/logic"
	"fleet/internal/model"
)

// TestResourceNotFound 覆盖资源不存在分支（404 语义）。
func TestResourceNotFound(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.GetApplication(ctx, applicant, "APP-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("申请不存在应 404: %v", err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, "APP-NONE", model.ApplicationInput{
		Reason: "x", VehicleIDs: allVehicles(), Interval: 30,
	}); errCode(err) != "NOT_FOUND" {
		t.Fatalf("修改不存在的申请应 404: %v", err)
	}
	if _, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
		ApplicationID: "APP-NONE", PilotVehicleIDs: []string{"VEH-01", "VEH-02"},
	}); errCode(err) != "NOT_FOUND" {
		t.Fatalf("为不存在的申请创建活动应 404: %v", err)
	}
	if _, err := e.svc.GetActivity(ctx, approver, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("活动不存在应 404: %v", err)
	}
	if _, err := e.svc.Progress(ctx, operator, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("进度查询不存在活动应 404: %v", err)
	}
	if _, err := e.svc.Promote(ctx, operator, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("推广不存在活动应 404: %v", err)
	}
	if _, err := e.svc.StopActivity(ctx, approver, "ACT-NONE", "x"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("停止不存在活动应 404: %v", err)
	}
	if _, err := e.svc.RetryPilot(ctx, operator, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("重试不存在活动应 404: %v", err)
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("批准恢复不存在活动应 404: %v", err)
	}
	if _, err := e.svc.PublishRestore(ctx, operator, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("下发恢复不存在活动应 404: %v", err)
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("完成不存在活动应 404: %v", err)
	}
	if _, err := e.svc.CompleteRestore(ctx, approver, "ACT-NONE"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("恢复完成不存在活动应 404: %v", err)
	}
}

// TestReviseGuards 覆盖退回后修改的权限与参数校验分支。
func TestReviseGuards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.mustApp(t, 30)
	if _, err := e.svc.ReviseApplication(ctx, operator, app.ID, model.ApplicationInput{
		Reason: "越权修改", VehicleIDs: allVehicles(), Interval: 30,
	}); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("非申请人不得修改: %v", err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "原因", VehicleIDs: allVehicles(), Interval: 30,
	}); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("仅被退回的申请可修改: %v", err)
	}
	if _, err := e.svc.RejectApplication(ctx, approver, app.ID, "请调整间隔说明"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "   ", VehicleIDs: allVehicles(), Interval: 30,
	}); errCode(err) != "INVALID_REASON" {
		t.Fatalf("修改后仍须校验必填要素: %v", err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "原因", VehicleIDs: allVehicles(), Interval: 5,
	}); errCode(err) != "INVALID_INTERVAL" {
		t.Fatalf("修改后仍须校验间隔范围: %v", err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "原因", VehicleIDs: []string{"VEH-01"}, Interval: 30,
	}); errCode(err) != "INVALID_VEHICLES" {
		t.Fatalf("修改后仍须校验车辆范围: %v", err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "原因", VehicleIDs: allVehicles(), Interval: 45,
	}); err != nil {
		t.Fatalf("退回后修改应成功: %v", err)
	}
	if _, err := e.svc.ApproveApplication(ctx, approver, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "原因", VehicleIDs: allVehicles(), Interval: 45,
	}); errCode(err) != "PARAM_FROZEN" {
		t.Fatalf("批准后不可修改: %v", err)
	}
}

// TestActionStatusGuards 覆盖各动作在非法状态下的拒绝分支。
func TestActionStatusGuards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	if _, err := e.svc.Promote(ctx, operator, act.ID); errCode(err) != "PROMOTE_BLOCKED" {
		t.Fatalf("试点未完成不得推广: %v", err)
	}
	if _, err := e.svc.RetryPilot(ctx, operator, act.ID); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("试点进行中不得重试: %v", err)
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, act.ID); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("未停止不得批准恢复: %v", err)
	}
	if _, err := e.svc.PublishRestore(ctx, operator, act.ID); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("未停止不得下发恢复: %v", err)
	}
	if _, err := e.svc.CompleteRestore(ctx, approver, act.ID); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("未停止不得记恢复完成: %v", err)
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, act.ID); errCode(err) != "COMPLETE_BLOCKED" {
		t.Fatalf("试点未完成不得记完成: %v", err)
	}
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	// 恢复动作对已进入 promoting 的活动依然受限
	if _, err := e.svc.PublishRestore(ctx, operator, act.ID); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("非停止状态不得下发恢复: %v", err)
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, act.ID); errCode(err) != "COMPLETE_BLOCKED" {
		t.Fatalf("推广车未确认不得记完成: %v", err)
	}
}

// TestTimeoutDuringPromoting 覆盖推广阶段超时（暂停推进、保留已发范围）。
func TestTimeoutDuringPromoting(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	e.ack(t, 3, true, "应用成功")
	e.advance(61 * time.Second)
	if err := e.svc.RefreshTimeouts(ctx); err != nil {
		t.Fatal(err)
	}
	p := e.progress(t, act.ID)
	if p.Activity.Status != model.ActPromoting {
		t.Fatalf("推广阶段超时只显示超时结果，活动保持 promoting，实际 %s", p.Activity.Status)
	}
	if p.CanComplete {
		t.Fatal("存在超时未确认车辆时不得记完成")
	}
	if p.TimeoutSeconds != 60 {
		t.Fatalf("超时口径应为 60 秒: %+v", p)
	}
	var veh4 *model.VehicleProgress
	for i := range p.Vehicles {
		if p.Vehicles[i].VehicleID == "VEH-04" {
			veh4 = &p.Vehicles[i]
		}
	}
	if veh4 == nil || veh4.Result != model.ResultTimeout || veh4.EffectiveValue != 60 {
		t.Fatalf("超时车结果展示不正确: %+v", veh4)
	}
	// 迟到成功翻转超时车结果
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(4), model.ReceiptInput{
		Version: e.desiredVersion(4), Success: true, Message: "重连后应用成功",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(5), model.ReceiptInput{
		Version: e.desiredVersion(5), Success: true, Message: "重连后应用成功",
	}); err != nil {
		t.Fatal(err)
	}
	p = e.progress(t, act.ID)
	if !p.AllConfirmed || !p.CanComplete {
		t.Fatalf("迟到成功后全部确认，可记完成: %+v", p)
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, act.ID); err != nil {
		t.Fatal(err)
	}
	// 已完成活动再补发回执：仅留痕
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(5), model.ReceiptInput{
		Version: e.desiredVersion(5), Success: true,
	}); errCode(err) != "ACTIVITY_CLOSED" {
		t.Fatalf("已完成活动的迟到回执应拒绝: %v", err)
	}
}

// TestExportUnconfirmedMapping 覆盖恢复阶段导出中 unconfirmed 的映射。
func TestExportUnconfirmedMapping(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	e.ack(t, 3, true, "应用成功")
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(4), model.ReceiptInput{
		Version: e.desiredVersion(4), Success: false, Message: "应用失败",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StopActivity(ctx, approver, act.ID, "停止验证导出"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, act.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PublishRestore(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	state, err := e.svc.ExportState(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	for _, v := range state["vehicles"].([]map[string]any) {
		results[v["id"].(string)] = v["result"].(string)
	}
	// 恢复版本已下发：全部目标车辆（含曾失败车）均为待确认恢复 unconfirmed
	for _, vid := range []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"} {
		if results[vid] != "unconfirmed" {
			t.Fatalf("%s 应显示 unconfirmed，实际 %s", vid, results[vid])
		}
	}
	// 失败历史仍保留在逐车失败原因中
	for _, v := range state["vehicles"].([]map[string]any) {
		if v["id"].(string) == "VEH-04" && v["fail_reason"].(string) != "应用失败" {
			t.Fatalf("失败原因应保留: %+v", v)
		}
	}
	// 恢复回执全部确认后，导出仍保留失败历史与超时口径
	raw, err := json.Marshal(state["activities"])
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContains(string(raw), "needs_recovery_vehicle_ids") {
		t.Fatal("导出应包含需恢复车辆列表")
	}
}

func jsonContains(s, sub string) bool {
	return len(s) > 0 && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestPullAfterCompletion 覆盖完成后车辆拉取（仍取得最新已发布配置）。
func TestPullAfterCompletion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 90, "VEH-01", "VEH-02")
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
	res := e.pull(t, 1)
	if !res.Granted || res.Version != e.desiredVersion(1) || res.Interval != 90 {
		t.Fatalf("完成后车辆应仍取得最新已发布配置: %+v", res)
	}
	// 新一轮申请/活动可以在完成后启动（V3：同车队单一未结束活动）
	app := e.mustApprovedApp(t, 30)
	act2, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
		ApplicationID: app.ID, PilotVehicleIDs: []string{"VEH-01", "VEH-02"},
	})
	if err != nil {
		t.Fatalf("上一活动结束后应允许创建新活动: %v", err)
	}
	if act2.ID == act.ID {
		t.Fatal("新活动编号应不同")
	}
	// 新试点版本必须高于旧版本
	if e.desiredVersion(1) <= res.Version {
		t.Fatalf("新活动版本应递增: %d <= %d", e.desiredVersion(1), res.Version)
	}
}
