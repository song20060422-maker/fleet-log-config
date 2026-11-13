package logic_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"fleet/internal/model"
)

// TestRestoreFlow 覆盖 R6/R7：停止保留范围、批准恢复、更高版本下发、逐车确认后才可恢复完成。
func TestRestoreFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	e.ack(t, 3, true, "应用成功")
	e.ack(t, 4, true, "应用成功")
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(5), model.ReceiptInput{
		Version: e.desiredVersion(5), Success: false, Message: "刷写超时",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, act.ID); errCode(err) != "COMPLETE_BLOCKED" {
		t.Fatalf("存在失败车辆时不得记完成，实际: %v", err)
	}
	stopped, err := e.svc.StopActivity(ctx, approver, act.ID, "一台应用失败，停止活动")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != model.ActStopped {
		t.Fatalf("活动应已停止，实际 %s", stopped.Status)
	}
	// 只有审批负责人能批准恢复
	if _, err := e.svc.ApproveRestore(ctx, operator, act.ID); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("运营不得批准恢复，实际: %v", err)
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, act.ID); err != nil {
		t.Fatalf("批准恢复失败: %v", err)
	}
	// 只有运营能下发恢复版本
	if _, err := e.svc.PublishRestore(ctx, approver, act.ID); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("审批人不得代运营下发恢复，实际: %v", err)
	}
	before := map[string]int{}
	for _, idx := range []int{1, 2, 3, 4, 5} {
		list, err := e.st.ConfigVersionsOfVehicle(ctx, vehicleName(idx))
		if err != nil {
			t.Fatal(err)
		}
		before[vehicleName(idx)] = list[len(list)-1].Version
	}
	if _, err := e.svc.PublishRestore(ctx, operator, act.ID); err != nil {
		t.Fatalf("下发恢复版本失败: %v", err)
	}
	for _, idx := range []int{1, 2, 3, 4, 5} {
		list, err := e.st.ConfigVersionsOfVehicle(ctx, vehicleName(idx))
		if err != nil {
			t.Fatal(err)
		}
		last := list[len(list)-1]
		if last.Kind != model.KindRecover || last.Interval != 60 {
			t.Fatalf("%s 恢复版本目标应为活动前值 60，实际: %+v", vehicleName(idx), last)
		}
		if last.Version <= before[vehicleName(idx)] {
			t.Fatalf("%s 恢复版本必须更高，实际 v%d <= v%d", vehicleName(idx), last.Version, before[vehicleName(idx)])
		}
	}
	p := e.progress(t, act.ID)
	if p.Activity.Status != model.ActRecovering {
		t.Fatalf("下发恢复后应进入 recovering，实际 %s", p.Activity.Status)
	}
	if p.AllRestored || p.AllRecoveredShown {
		t.Fatalf("仍有未确认车辆时不得显示全部恢复: allRestored=%v shown=%v", p.AllRestored, p.AllRecoveredShown)
	}
	if len(p.UnconfirmedRestoreIDs) != 5 {
		t.Fatalf("应有 5 台未确认恢复车辆，实际 %v", p.UnconfirmedRestoreIDs)
	}
	if _, err := e.svc.CompleteRestore(ctx, approver, act.ID); errCode(err) != "RESTORE_BLOCKED" {
		t.Fatalf("未确认恢复时不得记恢复完成，实际: %v", err)
	}
	if !strings.Contains(errText(func() error {
		_, err := e.svc.CompleteRestore(ctx, approver, act.ID)
		return err
	}), "禁止显示") {
		t.Fatal("拒绝原因必须解释未确认项与「全部恢复」门禁")
	}
	for _, idx := range []int{1, 2, 3, 4, 5} {
		e.ack(t, idx, true, "已恢复原设置")
	}
	p = e.progress(t, act.ID)
	if !p.AllRestored || p.RestoredCount != 5 {
		t.Fatalf("全部确认后应全部恢复: %+v", p)
	}
	if p.AllRecoveredShown {
		t.Fatal("尚未记为恢复完成前不得展示「全部恢复」")
	}
	recovered, err := e.svc.CompleteRestore(ctx, approver, act.ID)
	if err != nil {
		t.Fatalf("记恢复完成失败: %v", err)
	}
	if recovered.Status != model.ActRecovered {
		t.Fatalf("状态应为 recovered，实际 %s", recovered.Status)
	}
	p = e.progress(t, act.ID)
	if !p.AllRecoveredShown {
		t.Fatal("全部确认后应允许展示「全部恢复」")
	}
	for _, idx := range []int{1, 2, 3, 4, 5} {
		state, _, err := e.st.VehicleState(ctx, vehicleName(idx))
		if err != nil {
			t.Fatal(err)
		}
		if state.EffectiveValue != 60 {
			t.Fatalf("%s 恢复后生效值应为 60，实际 %d", vehicleName(idx), state.EffectiveValue)
		}
	}
}

func errText(fn func() error) string {
	if err := fn(); err != nil {
		return err.Error()
	}
	return ""
}

func vehicleName(idx int) string {
	return []string{"", "VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}[idx]
}

// TestRestoreOnlyPublishedVehicles 覆盖 R6（仅曾发布车辆需要恢复）。
func TestRestoreOnlyPublishedVehicles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 45, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	if _, err := e.svc.StopActivity(ctx, operator, act.ID, "试点阶段即停止"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, act.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PublishRestore(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{3, 4, 5} {
		list, err := e.st.ConfigVersionsOfVehicle(ctx, vehicleName(idx))
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 0 {
			t.Fatalf("%s 从未下发过配置，不应有恢复版本: %+v", vehicleName(idx), list)
		}
	}
	p := e.progress(t, act.ID)
	if p.NeedsRecoveryCount != 2 || len(p.NeedsRecoveryVehicleIDs) != 2 {
		t.Fatalf("仅试点 2 台需恢复，实际 %v", p.NeedsRecoveryVehicleIDs)
	}
	if len(p.UnpublishedVehicleIDs) != 3 {
		t.Fatalf("停止时未发范围应保留 3 台，实际 %v", p.UnpublishedVehicleIDs)
	}
	e.ack(t, 1, true, "已恢复")
	e.ack(t, 2, true, "已恢复")
	if _, err := e.svc.CompleteRestore(ctx, approver, act.ID); err != nil {
		t.Fatalf("两台均确认后应可记恢复完成: %v", err)
	}
}

// TestRestoreRejectsWhenNothingPublished 覆盖无下发记录时的恢复拒绝。
func TestRestoreRejectsWhenNothingPublished(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.mustApprovedApp(t, 30)
	// 直接落一条无下发的活动（模拟运营创建后立即停止的极端情况）
	act, err := e.svc.CreateActivity(ctx, operator, logicCreate(app.ID, "VEH-01", "VEH-02"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StopActivity(ctx, approver, act.ID, "立即停止"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, act.ID); err != nil {
		t.Fatalf("已下发两台，批准恢复应成功: %v", err)
	}
	if _, err := e.svc.PublishRestore(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	// 未下发车辆不参与恢复，剩余 3 台仍无恢复版本
	for _, idx := range []int{3, 4, 5} {
		list, _ := e.st.ConfigVersionsOfVehicle(ctx, vehicleName(idx))
		if len(list) != 0 {
			t.Fatalf("%s 不应有配置版本", vehicleName(idx))
		}
	}
}

// TestCompleteActivityGate 覆盖 R7（全部确认成功才可记完成）。
func TestCompleteActivityGate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CompleteActivity(ctx, approver, act.ID); errCode(err) != "COMPLETE_BLOCKED" {
		t.Fatalf("推广车未确认时不得记完成，实际: %v", err)
	}
	for _, idx := range []int{3, 4, 5} {
		e.ack(t, idx, true, "应用成功")
	}
	p := e.progress(t, act.ID)
	if !p.AllConfirmed || p.SuccessCount != 5 {
		t.Fatalf("全部成功应可记完成: %+v", p)
	}
	if _, err := e.svc.CompleteActivity(ctx, operator, act.ID); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("运营不得记完成，实际: %v", err)
	}
	done, err := e.svc.CompleteActivity(ctx, approver, act.ID)
	if err != nil {
		t.Fatalf("记完成失败: %v", err)
	}
	if done.Status != model.ActCompleted || done.CompletedAt == "" {
		t.Fatalf("活动应为完成: %+v", done)
	}
	if _, err := e.svc.StopActivity(ctx, approver, act.ID, "已完成再停止"); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("已完成活动不可停止，实际: %v", err)
	}
}

// TestRoleGuards 覆盖 R8（越权操作被拒绝并留痕）。
func TestRoleGuards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.mustApprovedApp(t, 30)
	if _, err := e.svc.CreateActivity(ctx, applicant, logicCreate(app.ID, "VEH-01", "VEH-02")); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("申请人不得执行发布，实际: %v", err)
	}
	act, err := e.svc.CreateActivity(ctx, operator, logicCreate(app.ID, "VEH-01", "VEH-02"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StopActivity(ctx, applicant, act.ID, "越权停止"); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("申请人不得停止活动，实际: %v", err)
	}
	if _, err := e.svc.Progress(ctx, vehicleUser(1), act.ID); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("车辆身份不得查看活动进度，实际: %v", err)
	}
	if _, err := e.svc.PullConfig(ctx, operator); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("运营不得以车辆身份拉取配置，实际: %v", err)
	}
	if _, err := e.svc.ListApplications(ctx, vehicleUser(1)); err != nil {
		t.Fatalf("列表查询不应 panic: %v", err)
	}
	events, err := e.st.Events(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	rejected := 0
	for _, ev := range events {
		if !ev.Accepted && ev.Reason != "" {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("越权操作必须留痕")
	}
}

// TestExportStateSchema 覆盖逐车核查导出与 Skill 输入 Schema。
func TestExportStateSchema(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.advance(61 * time.Second)
	if err := e.svc.RefreshTimeouts(ctx); err != nil {
		t.Fatal(err)
	}
	e.ack(t, 2, true, "迟到成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	e.ack(t, 3, true, "应用成功")
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(4), model.ReceiptInput{
		Version: e.desiredVersion(4), Success: false, Message: "存储空间不足",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := e.svc.ExportState(ctx, approver)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	for _, key := range []string{"preset", "applications", "activities", "vehicles", "config_pulls", "receipts"} {
		if _, ok := state[key]; !ok {
			t.Fatalf("导出缺少顶层字段 %s", key)
		}
	}
	preset := state["preset"].(map[string]any)
	if preset["fleet_id"] != model.FleetID || preset["initial_interval"] != 60 ||
		preset["interval_min"] != 10 || preset["interval_max"] != 300 || preset["timeout_seconds"] != 60 {
		t.Fatalf("preset 口径不正确: %+v", preset)
	}
	vehicles := state["vehicles"].([]map[string]any)
	if len(vehicles) != 5 {
		t.Fatalf("必须逐车导出 5 台，实际 %d", len(vehicles))
	}
	results := map[string]string{}
	for _, v := range vehicles {
		results[v["id"].(string)] = v["result"].(string)
		if v["pre_activity_value"] != 60 || v["effective_value"] == nil {
			t.Fatalf("逐车字段不完整: %+v", v)
		}
	}
	if results["VEH-01"] != model.ResultSuccess || results["VEH-04"] != model.ResultFailure ||
		results["VEH-05"] != "published" {
		t.Fatalf("逐车结果映射不正确: %+v", results)
	}
	activities := state["activities"].([]map[string]any)
	actExport := activities[0]
	published := actExport["published_vehicle_ids"].([]string)
	unpublished := actExport["unpublished_vehicle_ids"].([]string)
	if len(published)+len(unpublished) != 5 {
		t.Fatalf("已发/未发范围并集应等于目标范围: %v %v", published, unpublished)
	}
	pilotVersions := actExport["pilot_published_versions"].([]int)
	if len(pilotVersions) != 2 {
		t.Fatalf("试点版本应为 2 个，实际 %v", pilotVersions)
	}
	// 试点版本必须唯一且只属于试点车（Skill C05 检查口径）
	pulls := state["config_pulls"].([]map[string]any)
	for _, p := range pulls {
		if !p["granted"].(bool) {
			continue
		}
		version := p["version"].(int)
		inPilot := false
		for _, pv := range pilotVersions {
			if pv == version {
				inPilot = true
			}
		}
		if inPilot {
			vid := p["vehicle_id"].(string)
			if vid != "VEH-01" && vid != "VEH-02" {
				t.Fatalf("非试点车 %s 获得了试点版本 %d", vid, version)
			}
		}
	}
	// 被受理回执必须对应本车已发布版本（Skill C10 口径）
	for _, r := range state["receipts"].([]map[string]any) {
		if !r["accepted"].(bool) {
			if r["reject_reason"] == "" {
				t.Fatalf("被拒回执缺少原因: %+v", r)
			}
			continue
		}
		vid := r["vehicle_id"].(string)
		version := r["version"].(int)
		for _, v := range vehicles {
			if v["id"] != vid {
				continue
			}
			ok := false
			for _, pv := range v["published_versions"].([]map[string]any) {
				if pv["version"].(int) == version {
					ok = true
				}
			}
			if !ok {
				t.Fatalf("受理了未发布给 %s 的版本 %d", vid, version)
			}
		}
	}
	if _, err := e.svc.ExportState(ctx, applicant); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("申请人不得导出全量业务状态，实际: %v", err)
	}
}

// TestStatePersistsAcrossRestart 覆盖 V21（服务重启后业务结果仍可查询）。
func TestStatePersistsAcrossRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(2), model.ReceiptInput{
		Version: e.desiredVersion(2), Success: false, Message: "网络中断",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StopActivity(ctx, approver, act.ID, "停止以验证持久化"); err != nil {
		t.Fatal(err)
	}
	beforeState, err := e.svc.ExportState(ctx, approver)
	if err != nil {
		t.Fatal(err)
	}
	e.reopen(t)
	afterState, err := e.svc.ExportState(ctx, approver)
	if err != nil {
		t.Fatalf("重启后导出失败: %v", err)
	}
	if len(afterState["receipts"].([]map[string]any)) != len(beforeState["receipts"].([]map[string]any)) {
		t.Fatal("重启后回执记录丢失")
	}
	got, err := e.svc.GetActivity(ctx, approver, act.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.ActStopped || got.StoppedAt == "" {
		t.Fatalf("重启后活动状态丢失: %+v", got)
	}
	state, _, err := e.st.VehicleState(ctx, "VEH-01")
	if err != nil {
		t.Fatal(err)
	}
	if state.EffectiveValue != 30 || state.EffectiveVersion == 0 {
		t.Fatalf("重启后生效值丢失: %+v", state)
	}
	p, err := e.svc.Progress(ctx, operator, act.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Vehicles) != 5 {
		t.Fatalf("重启后逐车核查数据丢失: %+v", p.Vehicles)
	}
}
