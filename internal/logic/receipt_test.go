package logic_test

import (
	"context"
	"testing"
	"time"

	"fleet/internal/model"
)

// TestVersionAndEffectiveModel 覆盖 R3：车辆只取得面向自己的最新版本、每车版本递增、
// 下发只写期望值不改生效值。
func TestVersionAndEffectiveModel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")

	first := e.pull(t, 1)
	if !first.Granted || first.Interval != 30 || first.Kind != model.KindPilot {
		t.Fatalf("试点车应取得 30 秒配置，实际: %+v", first)
	}
	state, _, err := e.st.VehicleState(ctx, "VEH-01")
	if err != nil {
		t.Fatal(err)
	}
	if state.DesiredValue != 30 || state.EffectiveValue != 60 {
		t.Fatalf("下发只改期望值，生效值应保持 60，实际: %+v", state)
	}
	// 重复拉取仍返回同一最新版本，不产生新版本
	again := e.pull(t, 1)
	if again.Version != first.Version {
		t.Fatalf("重复拉取应返回最新版本 %d，实际 %d", first.Version, again.Version)
	}
	// 其他车版本独立递增，且不会返回别人的版本
	second := e.pull(t, 2)
	if second.Version <= first.Version {
		t.Fatalf("版本应全局单调递增以便区分批次，实际 v%d / v%d", first.Version, second.Version)
	}
	if res := e.pull(t, 4); res.Granted {
		t.Fatal("未下发车辆不得获得配置")
	}

	res := e.ack(t, 1, true, "应用成功")
	if !res.Accepted || !res.Recorded || res.EffectiveValue != 30 {
		t.Fatalf("成功回执应更新生效值，实际: %+v", res)
	}
	state, _, err = e.st.VehicleState(ctx, "VEH-01")
	if err != nil {
		t.Fatal(err)
	}
	if state.EffectiveVersion != first.Version || state.EffectiveValue != 30 {
		t.Fatalf("生效值应指向成功回执版本，实际: %+v", state)
	}
	published, err := e.st.ConfigVersionsOfVehicle(ctx, "VEH-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].Interval != 30 || published[0].ActivityID != act.ID {
		t.Fatalf("下发记录不正确: %+v", published)
	}
}

// TestDuplicateSuccessIsIdempotent 覆盖 R4（重复成功不重复记账）。
func TestDuplicateSuccessIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.mustActivity(t, 30, "VEH-01", "VEH-02")
	first := e.ack(t, 1, true, "应用成功")
	second := e.ack(t, 1, true, "重复上报")
	if !first.Recorded {
		t.Fatal("首次成功应记账")
	}
	if second.Recorded {
		t.Fatal("重复成功不应重复记账")
	}
	if second.EffectiveValue != 30 || second.Result != model.ResultSuccess {
		t.Fatalf("重复成功不得改变生效状态: %+v", second)
	}
	receipts, err := e.st.Receipts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recorded := 0
	for _, r := range receipts {
		if r.Accepted && r.Outcome == "success" && r.Recorded {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("同一 (车, 版本) 成功只应记账一次，实际 %d 次", recorded)
	}
}

// TestRejectUnknownAndCrossVehicleReceipts 覆盖 R4/R8（未知版本、跨车、越权身份）。
func TestRejectUnknownAndCrossVehicleReceipts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.mustActivity(t, 30, "VEH-01", "VEH-02")

	// 未知版本（该车从未发布过）
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(1), model.ReceiptInput{
		Version: 999, Success: true,
	}); errCode(err) != "UNKNOWN_VERSION" {
		t.Fatalf("未知版本必须被拒绝，实际: %v", err)
	}
	// 跨车：VEH-02 的版本由 VEH-01 上报
	state2, _, _ := e.st.VehicleState(ctx, "VEH-02")
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(1), model.ReceiptInput{
		Version: state2.DesiredVersion, Success: true,
	}); errCode(err) != "CROSS_VEHICLE" {
		t.Fatalf("跨车回执必须被拒绝，实际: %v", err)
	}
	// 跨车：凭证明明是 VEH-01，却在请求体声称 VEH-02
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(1), model.ReceiptInput{
		VehicleID: "VEH-02", Version: 1, Success: true,
	}); errCode(err) != "CROSS_VEHICLE" {
		t.Fatalf("伪造车辆编号必须被拒绝，实际: %v", err)
	}
	// 角色越权
	if _, err := e.svc.ReportReceipt(ctx, operator, model.ReceiptInput{Version: 1, Success: true}); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("非车辆身份不得上报回执，实际: %v", err)
	}
	// 拒绝必须留痕且带原因
	receipts, err := e.st.Receipts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rejected := 0
	for _, r := range receipts {
		if !r.Accepted {
			rejected++
			if r.RejectReason == "" {
				t.Fatalf("被拒绝回执必须记录原因: %+v", r)
			}
		}
	}
	if rejected < 3 {
		t.Fatalf("应至少留痕 3 条被拒回执，实际 %d", rejected)
	}
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(1), model.ReceiptInput{Version: 0, Success: true}); errCode(err) != "INVALID_VERSION" {
		t.Fatalf("非法版本号应被拒绝，实际: %v", err)
	}
}

// TestStaleVersionDoesNotOverwriteEffective 覆盖 R4/R5（旧版本迟到回执不覆盖新生效值）。
func TestStaleVersionDoesNotOverwriteEffective(t *testing.T) {
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
	e.ack(t, 5, true, "应用成功")
	if _, err := e.svc.StopActivity(ctx, approver, act.ID, "验证旧回执"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ApproveRestore(ctx, approver, act.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PublishRestore(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	state, _, err := e.st.VehicleState(ctx, "VEH-01")
	if err != nil {
		t.Fatal(err)
	}
	if state.DesiredValue != 60 || state.DesiredVersion <= 5 {
		t.Fatalf("恢复版本应更高且目标为活动前值 60，实际: %+v", state)
	}
	// 恢复回执到达前，上报旧的试点版本成功：拒绝且生效值不变（仍为 30 / 旧成功版本）
	before := state.EffectiveVersion
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(1), model.ReceiptInput{
		Version: 1, Success: true,
	}); errCode(err) != "STALE_VERSION" {
		t.Fatalf("旧版本迟到回执必须被拒绝，实际: %v", err)
	}
	state, _, _ = e.st.VehicleState(ctx, "VEH-01")
	if state.EffectiveVersion != before || state.EffectiveValue != 30 {
		t.Fatalf("旧回执不得覆盖生效值，实际: %+v", state)
	}
}

// TestTimeoutLateSuccessKeepsHistory 覆盖 R5（超时显示、迟到成功翻转并保留超时历史）。
func TestTimeoutLateSuccessKeepsHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.advance(61 * time.Second)
	if err := e.svc.RefreshTimeouts(ctx); err != nil {
		t.Fatal(err)
	}
	p := e.progress(t, act.ID)
	if p.Activity.Status != model.ActPaused {
		t.Fatalf("试点超时后活动应暂停，实际 %s", p.Activity.Status)
	}
	var veh2 *model.VehicleProgress
	for i := range p.Vehicles {
		if p.Vehicles[i].VehicleID == "VEH-02" {
			veh2 = &p.Vehicles[i]
		}
	}
	if veh2 == nil || veh2.Result != model.ResultTimeout || len(veh2.TimeoutHistory) != 1 {
		t.Fatalf("VEH-02 应显示超时并保留历史: %+v", veh2)
	}
	// 当前期望版本迟到成功：结果翻转为成功，超时历史保留
	late := e.ack(t, 2, true, "重连后应用成功")
	if !late.Accepted || late.Result != model.ResultSuccess {
		t.Fatalf("当前版本迟到成功应被受理，实际: %+v", late)
	}
	p = e.progress(t, act.ID)
	if p.Activity.Status != model.ActPromotionReady {
		t.Fatalf("迟到成功后应可推广，实际 %s", p.Activity.Status)
	}
	veh2 = nil
	for i := range p.Vehicles {
		if p.Vehicles[i].VehicleID == "VEH-02" {
			veh2 = &p.Vehicles[i]
		}
	}
	if veh2 == nil || veh2.Result != model.ResultSuccess {
		t.Fatalf("迟到成功应翻转结果: %+v", veh2)
	}
	if len(veh2.TimeoutHistory) != 1 || !veh2.TimeoutHistory[0].ResolvedByLateSuccess {
		t.Fatalf("超时历史必须保留并标记迟到成功: %+v", veh2.TimeoutHistory)
	}
}

// TestFailureKeepsEffectiveValue 覆盖 R5（失败回执不改变此前生效值）。
func TestFailureKeepsEffectiveValue(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	e.ack(t, 2, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); err != nil {
		t.Fatal(err)
	}
	state3, _, _ := e.st.VehicleState(ctx, "VEH-03")
	res, err := e.svc.ReportReceipt(ctx, vehicleUser(3), model.ReceiptInput{
		Version: state3.DesiredVersion, Success: false, Message: "配置解析失败",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.EffectiveValue != 60 || res.Result != model.ResultFailure {
		t.Fatalf("失败回执不得改变生效值: %+v", res)
	}
	state3, _, _ = e.st.VehicleState(ctx, "VEH-03")
	if state3.FailReason != "配置解析失败" {
		t.Fatalf("失败原因必须保留: %+v", state3)
	}
	p := e.progress(t, act.ID)
	found := false
	for _, v := range p.Vehicles {
		if v.VehicleID == "VEH-03" && v.Result == model.ResultFailure && v.FailReason != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("逐车进度必须展示失败与原因")
	}
	if p.CanComplete {
		t.Fatal("存在失败车辆时不得记为完成")
	}
}

// TestStoppedActivityNotRestartedByLateReceipt 覆盖 R5（已停止活动不因迟到回执重启）。
func TestStoppedActivityNotRestartedByLateReceipt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	e.ack(t, 1, true, "应用成功")
	if _, err := e.svc.StopActivity(ctx, approver, act.ID, "停止未发送部分"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(2), model.ReceiptInput{
		Version: 2, Success: true, Message: "停机后迟到回执",
	}); errCode(err) != "ACTIVITY_CLOSED" {
		t.Fatalf("已停止活动的迟到回执应仅留痕，实际: %v", err)
	}
	got, err := e.svc.GetActivity(ctx, approver, act.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.ActStopped {
		t.Fatalf("活动状态不得因迟到回执改变，实际 %s", got.Status)
	}
}
