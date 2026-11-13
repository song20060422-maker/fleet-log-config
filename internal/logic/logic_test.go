package logic_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fleet/internal/logic"
	"fleet/internal/model"
	"fleet/internal/store"
)

// env 测试环境：独立 SQLite 文件 + 可注入时钟。
type env struct {
	t    *testing.T
	svc  *logic.Service
	st   *store.Store
	link string
	now  time.Time
}

var (
	applicant = model.User{ID: "dev", Username: "dev", Name: "研发申请人", Role: model.RoleApplicant}
	approver  = model.User{ID: "approver", Username: "approver", Name: "审批负责人", Role: model.RoleApprover}
	operator  = model.User{ID: "ops", Username: "ops", Name: "发布运营", Role: model.RoleOperator}
)

func vehicleUser(i int) model.User {
	return model.User{
		ID: fmt.Sprintf("vehicle-%02d", i), Username: fmt.Sprintf("vehicle-%02d", i),
		Name: fmt.Sprintf("模拟车辆(VEH-%02d)", i), Role: model.RoleVehicle,
		VehicleID: fmt.Sprintf("VEH-%02d", i),
	}
}

func allVehicles() []string {
	return []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	link := "sqlite::@file(" + filepath.Join(dir, "fleet.db") + ")"
	st, err := store.Open(link)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Init(context.Background()); err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	e := &env{t: t, svc: logic.New(st, 60*time.Second), st: st, link: link,
		now: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	e.svc.SetNowFn(func() time.Time { return e.now })
	t.Cleanup(func() { _ = e.st.Close(context.Background()) })
	return e
}

func (e *env) advance(d time.Duration) { e.now = e.now.Add(d) }

// reopen 关闭并重新打开同一数据库文件，模拟服务重启（V21）。
func (e *env) reopen(t *testing.T) {
	t.Helper()
	if err := e.st.Close(context.Background()); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	st, err := store.Open(e.link)
	if err != nil {
		t.Fatalf("重新打开数据库失败: %v", err)
	}
	if err := st.Init(context.Background()); err != nil {
		t.Fatalf("重启后初始化失败: %v", err)
	}
	e.st = st
	e.svc = logic.New(st, 60*time.Second)
	e.svc.SetNowFn(func() time.Time { return e.now })
}

func (e *env) mustApp(t *testing.T, interval int) *model.Application {
	t.Helper()
	app, err := e.svc.CreateApplication(context.Background(), applicant, model.ApplicationInput{
		Reason: "排查车机日志丢失", VehicleIDs: allVehicles(), Interval: interval,
	})
	if err != nil {
		t.Fatalf("创建申请失败: %v", err)
	}
	return app
}

func (e *env) mustApprovedApp(t *testing.T, interval int) *model.Application {
	t.Helper()
	app := e.mustApp(t, interval)
	approved, err := e.svc.ApproveApplication(context.Background(), approver, app.ID)
	if err != nil {
		t.Fatalf("批准申请失败: %v", err)
	}
	return approved
}

func (e *env) mustActivity(t *testing.T, interval int, pilots ...string) *model.Activity {
	t.Helper()
	app := e.mustApprovedApp(t, interval)
	act, err := e.svc.CreateActivity(context.Background(), operator, logic.CreateActivityInput{
		ApplicationID: app.ID, PilotVehicleIDs: pilots,
	})
	if err != nil {
		t.Fatalf("创建活动失败: %v", err)
	}
	return act
}

// ack 以指定车辆身份上报成功回执（版本取该车当前期望版本）。
func (e *env) ack(t *testing.T, idx int, success bool, msg string) *model.ReceiptResult {
	t.Helper()
	state, ok, err := e.st.VehicleState(context.Background(), fmt.Sprintf("VEH-%02d", idx))
	if err != nil || !ok {
		t.Fatalf("读取车辆状态失败: %v", err)
	}
	res, err := e.svc.ReportReceipt(context.Background(), vehicleUser(idx), model.ReceiptInput{
		Version: state.DesiredVersion, Success: success, Message: msg,
	})
	if err != nil {
		t.Fatalf("上报回执失败: %v", err)
	}
	return res
}

func (e *env) pull(t *testing.T, idx int) *model.PullResult {
	t.Helper()
	res, err := e.svc.PullConfig(context.Background(), vehicleUser(idx))
	if err != nil {
		t.Fatalf("拉取配置失败: %v", err)
	}
	return res
}

func (e *env) progress(t *testing.T, id string) *model.ActivityProgress {
	t.Helper()
	p, err := e.svc.Progress(context.Background(), operator, id)
	if err != nil {
		t.Fatalf("读取进度失败: %v", err)
	}
	return p
}

func errCode(err error) string {
	if bizErr, ok := err.(*model.BizError); ok {
		return bizErr.Code
	}
	return ""
}

// desiredVersion 读取车辆当前期望版本（回执必须对应此版本）。
func (e *env) desiredVersion(idx int) int {
	e.t.Helper()
	st, ok, err := e.st.VehicleState(context.Background(), vehicleName(idx))
	if err != nil || !ok {
		e.t.Fatalf("读取 %s 期望版本失败: %v", vehicleName(idx), err)
	}
	return st.DesiredVersion
}

func logicCreate(appID string, pilots ...string) logic.CreateActivityInput {
	return logic.CreateActivityInput{ApplicationID: appID, PilotVehicleIDs: pilots}
}

// ---------------------------------------------------------------- R1 申请与审批

func TestApplicationIntervalBoundary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, interval := range []int{model.IntervalMin, model.IntervalMax} {
		app, err := e.svc.CreateApplication(ctx, applicant, model.ApplicationInput{
			Reason: "边界值校验", VehicleIDs: allVehicles(), Interval: interval,
		})
		if err != nil {
			t.Fatalf("间隔 %d 应被接受，实际错误: %v", interval, err)
		}
		if app.Status != model.AppPending {
			t.Fatalf("新申请状态应为 pending，实际 %s", app.Status)
		}
	}
	for _, interval := range []int{model.IntervalMin - 1, model.IntervalMax + 1, 0, -5} {
		_, err := e.svc.CreateApplication(ctx, applicant, model.ApplicationInput{
			Reason: "边界值校验", VehicleIDs: allVehicles(), Interval: interval,
		})
		if errCode(err) != "INVALID_INTERVAL" {
			t.Fatalf("间隔 %d 应被拒绝，实际: %v", interval, err)
		}
	}
}

func TestApplicationRequiredElements(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cases := []struct {
		name string
		in   model.ApplicationInput
		code string
	}{
		{"缺原因", model.ApplicationInput{Reason: "   ", VehicleIDs: allVehicles(), Interval: 30}, "INVALID_REASON"},
		{"少车辆", model.ApplicationInput{Reason: "原因", VehicleIDs: []string{"VEH-01", "VEH-02"}, Interval: 30}, "INVALID_VEHICLES"},
		{"重复车辆", model.ApplicationInput{Reason: "原因", VehicleIDs: []string{"VEH-01", "VEH-01", "VEH-03", "VEH-04", "VEH-05"}, Interval: 30}, "INVALID_VEHICLES"},
		{"多车辆", model.ApplicationInput{Reason: "原因", VehicleIDs: []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05", "VEH-09"}, Interval: 30}, "INVALID_VEHICLES"},
	}
	for _, c := range cases {
		if _, err := e.svc.CreateApplication(ctx, applicant, c.in); errCode(err) != c.code {
			t.Fatalf("%s: 期望错误码 %s，实际 %v", c.name, c.code, err)
		}
	}
	if _, err := e.svc.CreateApplication(ctx, operator, model.ApplicationInput{
		Reason: "越权申请", VehicleIDs: allVehicles(), Interval: 30,
	}); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("运营不应能创建申请，实际: %v", err)
	}
}

func TestSelfApprovalRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.mustApp(t, 30)
	if _, err := e.svc.ApproveApplication(ctx, applicant, app.ID); errCode(err) != "SELF_APPROVAL" {
		t.Fatalf("申请人自审批必须被拒绝，实际: %v", err)
	}
	if _, err := e.svc.RejectApplication(ctx, applicant, app.ID, "自审退回"); errCode(err) != "SELF_APPROVAL" {
		t.Fatalf("申请人自退回必须被拒绝，实际: %v", err)
	}
	events, err := e.st.Events(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Action == "application.approve" && !ev.Accepted && ev.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("自审批拒绝必须留痕")
	}
}

func TestRejectReviseResubmitAndFreeze(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.mustApp(t, 30)
	if _, err := e.svc.RejectApplication(ctx, approver, app.ID, "间隔偏大，请改为 20 秒"); err != nil {
		t.Fatalf("退回失败: %v", err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "排查车机日志丢失", VehicleIDs: allVehicles(), Interval: 20,
	}); err != nil {
		t.Fatalf("退回后修改失败: %v", err)
	}
	revised, err := e.svc.GetApplication(ctx, applicant, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revised.Status != model.AppPending || revised.Interval != 20 || revised.RejectReason != "" {
		t.Fatalf("修改后应回到待审批且参数更新，实际: %+v", revised)
	}
	if _, err := e.svc.ApproveApplication(ctx, approver, app.ID); err != nil {
		t.Fatalf("重新审批失败: %v", err)
	}
	if _, err := e.svc.ReviseApplication(ctx, applicant, app.ID, model.ApplicationInput{
		Reason: "批准后改参数", VehicleIDs: allVehicles(), Interval: 40,
	}); errCode(err) != "PARAM_FROZEN" {
		t.Fatalf("批准后参数不可原地修改，实际: %v", err)
	}
	if _, err := e.svc.ApproveApplication(ctx, approver, app.ID); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("重复批准应被拒绝，实际: %v", err)
	}
	if _, err := e.svc.RejectApplication(ctx, approver, app.ID, "再退回"); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("已批准申请不可退回，实际: %v", err)
	}
	if _, err := e.svc.RejectApplication(ctx, approver, app.ID, "  "); errCode(err) != "INVALID_REASON" {
		t.Fatalf("退回必须填写原因，实际: %v", err)
	}
}

// ---------------------------------------------------------------- R2 活动与试点门禁

func TestActivityRequiresApprovedApplication(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.mustApp(t, 30)
	_, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
		ApplicationID: app.ID, PilotVehicleIDs: []string{"VEH-01", "VEH-02"},
	})
	if errCode(err) != "APP_NOT_APPROVED" {
		t.Fatalf("未批准申请不得创建活动，实际: %v", err)
	}
	if _, err := e.svc.CreateActivity(ctx, approver, logic.CreateActivityInput{
		ApplicationID: app.ID, PilotVehicleIDs: []string{"VEH-01", "VEH-02"},
	}); errCode(err) != "FORBIDDEN_ROLE" {
		t.Fatalf("审批负责人不能代运营执行发布，实际: %v", err)
	}
}

func TestActivityPilotValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.mustApprovedApp(t, 30)
	cases := []struct {
		name   string
		pilots []string
	}{
		{"仅一台", []string{"VEH-01"}},
		{"三台", []string{"VEH-01", "VEH-02", "VEH-03"}},
		{"重复", []string{"VEH-01", "VEH-01"}},
		{"越界", []string{"VEH-01", "VEH-09"}},
	}
	for _, c := range cases {
		if _, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
			ApplicationID: app.ID, PilotVehicleIDs: c.pilots,
		}); errCode(err) != "INVALID_PILOT" {
			t.Fatalf("%s: 期望 INVALID_PILOT，实际 %v", c.name, err)
		}
	}
}

func TestSingleOpenActivityPerFleet(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	second := e.mustApprovedApp(t, 45)
	if _, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
		ApplicationID: second.ID, PilotVehicleIDs: []string{"VEH-03", "VEH-04"},
	}); errCode(err) != "ACTIVITY_EXISTS" {
		t.Fatalf("同车队单一未结束活动，实际: %v", err)
	}
	if _, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
		ApplicationID: act.ApplicationID, PilotVehicleIDs: []string{"VEH-01", "VEH-02"},
	}); errCode(err) != "ACTIVITY_EXISTS" {
		t.Fatalf("同一申请不得重复创建活动，实际: %v", err)
	}
}

func TestConcurrentActivityCreationOnlyOneSucceeds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	apps := make([]*model.Application, 0, 8)
	for i := 0; i < 8; i++ {
		apps = append(apps, e.mustApprovedApp(t, 30+i))
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(app *model.Application) {
			defer wg.Done()
			if _, err := e.svc.CreateActivity(ctx, operator, logic.CreateActivityInput{
				ApplicationID: app.ID, PilotVehicleIDs: []string{"VEH-01", "VEH-02"},
			}); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(apps[i])
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("并发创建活动应只有 1 个成功，实际 %d", success)
	}
}

func TestPilotIsolationAndPromoteGate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")

	// 未试点车拉取不到配置（V4）
	pull := e.pull(t, 3)
	if pull.Granted {
		t.Fatal("未试点车不得提前获得活动配置")
	}
	if pull.RejectReason == "" {
		t.Fatal("拒绝必须带原因")
	}
	pulls, err := e.st.ConfigPulls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulls) != 1 || pulls[0].Granted || pulls[0].VehicleID != "VEH-03" {
		t.Fatalf("拉取记录不正确: %+v", pulls)
	}

	// 试点未全成功不得推广（V5）
	if _, err := e.svc.Promote(ctx, operator, act.ID); errCode(err) != "PROMOTE_BLOCKED" {
		t.Fatalf("试点未成功时推广必须被拒绝，实际: %v", err)
	}

	e.ack(t, 1, true, "应用成功")
	if _, err := e.svc.Promote(ctx, operator, act.ID); errCode(err) != "PROMOTE_BLOCKED" {
		t.Fatalf("仅一台试点成功时推广必须被拒绝，实际: %v", err)
	}
	e.ack(t, 2, true, "应用成功")
	p := e.progress(t, act.ID)
	if !p.CanPromote || p.Activity.Status != model.ActPromotionReady {
		t.Fatalf("两台试点成功后可推广，实际状态 %s canPromote=%v", p.Activity.Status, p.CanPromote)
	}
	if p.SuccessCount != 2 || len(p.UnpublishedVehicleIDs) != 3 {
		t.Fatalf("试点成功后已发/未发范围不正确: %+v", p)
	}

	promoted, err := e.svc.Promote(ctx, operator, act.ID)
	if err != nil {
		t.Fatalf("推广失败: %v", err)
	}
	if promoted.Status != model.ActPromoting {
		t.Fatalf("推广后状态应为 promoting，实际 %s", promoted.Status)
	}
	for _, idx := range []int{3, 4, 5} {
		if res := e.pull(t, idx); !res.Granted || res.Interval != 30 {
			t.Fatalf("推广后 VEH-%02d 应取得 30 秒配置，实际: %+v", idx, res)
		}
	}
	if _, err := e.svc.Promote(ctx, operator, act.ID); errCode(err) != "PROMOTE_BLOCKED" {
		t.Fatalf("重复推广应被拒绝，实际: %v", err)
	}
}

func TestPilotFailurePausesAndStopKeepsScope(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(2), model.ReceiptInput{
		Version: 2, Success: false, Message: "存储空间不足",
	}); err != nil {
		t.Fatalf("失败回执应被受理: %v", err)
	}
	p := e.progress(t, act.ID)
	if p.Activity.Status != model.ActPaused {
		t.Fatalf("试点失败后活动应暂停，实际 %s", p.Activity.Status)
	}
	if p.CanPromote {
		t.Fatal("暂停后不得推广")
	}
	if p.PromoteBlockReason == "" {
		t.Fatal("暂停原因必须可见")
	}
	// 未试点车始终未收到配置
	for _, idx := range []int{3, 4, 5} {
		if res := e.pull(t, idx); res.Granted {
			t.Fatalf("未试点车 VEH-%02d 不应获得配置", idx)
		}
	}
	stopped, err := e.svc.StopActivity(ctx, approver, act.ID, "试点失败，停止未发送部分")
	if err != nil {
		t.Fatalf("停止活动失败: %v", err)
	}
	if stopped.Status != model.ActStopped || stopped.StoppedAt == "" {
		t.Fatalf("停止后状态/时间不正确: %+v", stopped)
	}
	p = e.progress(t, act.ID)
	if len(p.PublishedVehicleIDs) != 2 || len(p.UnpublishedVehicleIDs) != 3 {
		t.Fatalf("停止后已发/未发范围必须保留: %+v", p)
	}
	// 停止 ≠ 撤销：已发车的期望值/生效记录保留
	state, _, err := e.st.VehicleState(ctx, "VEH-01")
	if err != nil {
		t.Fatal(err)
	}
	if state.DesiredVersion == 0 || state.EffectiveValue != 60 {
		t.Fatalf("停止后已下发与生效记录必须保留，实际: %+v", state)
	}
	if _, err := e.svc.StopActivity(ctx, approver, act.ID, "重复停止"); errCode(err) != "INVALID_STATUS" {
		t.Fatalf("重复停止应被拒绝，实际: %v", err)
	}
}

func TestRetryPilotAfterPause(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	act := e.mustActivity(t, 30, "VEH-01", "VEH-02")
	if _, err := e.svc.ReportReceipt(ctx, vehicleUser(1), model.ReceiptInput{
		Version: 1, Success: false, Message: "网络异常",
	}); err != nil {
		t.Fatal(err)
	}
	retried, err := e.svc.RetryPilot(ctx, operator, act.ID)
	if err != nil {
		t.Fatalf("暂停后重试试点失败: %v", err)
	}
	if retried.Status != model.ActPaused {
		t.Fatalf("重试后仍应等待回执（暂停），实际 %s", retried.Status)
	}
	e.ack(t, 1, true, "重试成功")
	e.ack(t, 2, true, "应用成功")
	p := e.progress(t, act.ID)
	if p.Activity.Status != model.ActPromotionReady {
		t.Fatalf("重试成功后应可推广，实际 %s", p.Activity.Status)
	}
}
