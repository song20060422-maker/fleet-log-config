package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"fleet/internal/model"
	"fleet/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	link := "sqlite::@file(" + filepath.Join(t.TempDir(), "store.db") + ")"
	st, err := store.Open(link)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Init(context.Background()); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

func TestOpenRejectsUnsupportedDriver(t *testing.T) {
	if _, err := store.Open("oracle:scott:tiger@127.0.0.1:1521/orcl"); err == nil {
		t.Fatal("不支持的数据库链接应直接返回错误")
	}
}

func TestDialectOf(t *testing.T) {
	cases := map[string]string{
		"mysql:root:root@tcp(127.0.0.1:3307)/fleet_log?charset=utf8mb4": store.DialectMySQL,
		"sqlite::@file(./data/fleet.db)":                                store.DialectSQLite,
		"MYSQL:root:root@tcp(127.0.0.1:3307)/fleet_log":                 store.DialectMySQL,
	}
	for link, want := range cases {
		if got := store.DialectOf(link); got != want {
			t.Fatalf("DialectOf(%q)=%q, 期望 %q", link, got, want)
		}
	}
}

func TestInitIsIdempotentAndSeedsPreset(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := st.Init(ctx); err != nil {
		t.Fatalf("重复初始化应幂等: %v", err)
	}
	vehicles, err := st.Vehicles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vehicles) != 5 {
		t.Fatalf("应预置 5 台车辆，实际 %d", len(vehicles))
	}
	offline := 0
	for _, v := range vehicles {
		if v.FleetID != model.FleetID || v.BaselineValue != 60 {
			t.Fatalf("车辆预置不正确: %+v", v)
		}
		if !v.Online {
			offline++
		}
	}
	if offline != 1 {
		t.Fatalf("应有一台离线试点车用于网络分支，实际 %d", offline)
	}
	for _, v := range vehicles {
		state, ok, err := st.VehicleState(ctx, v.ID)
		if err != nil || !ok {
			t.Fatalf("车辆状态缺失: %s %v", v.ID, err)
		}
		if state.EffectiveValue != 60 || state.EffectiveVersion != 0 || state.Result != model.ResultNotPublished {
			t.Fatalf("初始生效值应为 60: %+v", state)
		}
	}
	if len(store.PresetUsers()) != 8 {
		t.Fatalf("应预置 8 个账号（3 角色 + 5 车辆），实际 %d", len(store.PresetUsers()))
	}
}

func TestUserLookup(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u, ok, err := st.UserByToken(ctx, store.TokenFor("approver"))
	if err != nil || !ok {
		t.Fatalf("按 token 查询失败: %v", err)
	}
	if u.Role != model.RoleApprover || u.Username != "approver" {
		t.Fatalf("账号信息不正确: %+v", u)
	}
	if _, ok, _ := st.UserByToken(ctx, "tok-not-exist"); ok {
		t.Fatal("不存在的 token 不应命中")
	}
	login, ok, err := st.UserByCredentials(ctx, "vehicle-03", store.HashPassword(store.PresetPassword))
	if err != nil || !ok || login.VehicleID != "VEH-03" {
		t.Fatalf("车辆账号应绑定车辆: %+v %v", login, err)
	}
	if _, ok, _ := st.UserByCredentials(ctx, "vehicle-03", store.HashPassword("bad")); ok {
		t.Fatal("错误口令不应命中")
	}
}

func TestApplicationRepository(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	app := &model.Application{
		ID: "APP-000001", FleetID: model.FleetID, Reason: "排查日志缺失",
		Interval: 30, VehicleIDs: []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"},
		Applicant: "dev", Status: model.AppPending, CreatedAt: "2026-11-13T10:00:00Z",
		UpdatedAt: "2026-11-13T10:00:00Z",
	}
	if err := st.InsertApplication(ctx, app); err != nil {
		t.Fatalf("写入申请失败: %v", err)
	}
	got, ok, err := st.Application(ctx, app.ID)
	if err != nil || !ok {
		t.Fatalf("读取申请失败: %v", err)
	}
	if len(got.VehicleIDs) != 5 || got.Interval != 30 || got.Status != model.AppPending {
		t.Fatalf("申请读取不正确: %+v", got)
	}
	if err := st.UpdateApplication(ctx, app.ID, map[string]any{
		"status": model.AppApproved, "approver": "approver", "frozen": 1,
	}); err != nil {
		t.Fatalf("更新申请失败: %v", err)
	}
	got, _, _ = st.Application(ctx, app.ID)
	if got.Status != model.AppApproved || !got.Frozen || got.Approver != "approver" {
		t.Fatalf("申请更新未生效: %+v", got)
	}
	list, err := st.Applications(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("申请列表不正确: %v %d", err, len(list))
	}
	if _, ok, _ := st.Application(ctx, "APP-404"); ok {
		t.Fatal("不存在的申请不应命中")
	}
}

func TestActivityRepositoryAndOpenLookup(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	app := &model.Application{
		ID: "APP-000002", FleetID: model.FleetID, Reason: "原因", Interval: 20,
		VehicleIDs: []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"},
		Applicant:  "dev", Status: model.AppApproved, CreatedAt: "2026-11-13T10:00:00Z", UpdatedAt: "2026-11-13T10:00:00Z",
	}
	if err := st.InsertApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	act := &model.Activity{
		ID: "ACT-000001", ApplicationID: app.ID, FleetID: model.FleetID,
		Status: model.ActPiloting, Interval: 20,
		PilotVehicleIDs:  []string{"VEH-01", "VEH-02"},
		TargetVehicleIDs: []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"},
		CreatedBy:        "ops", CreatedAt: "2026-11-13T10:01:00Z", UpdatedAt: "2026-11-13T10:01:00Z",
	}
	if err := st.InsertActivity(ctx, act); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Activity(ctx, act.ID)
	if err != nil || !ok {
		t.Fatalf("读取活动失败: %v", err)
	}
	if len(got.PilotVehicleIDs) != 2 || len(got.TargetVehicleIDs) != 5 {
		t.Fatalf("车辆范围读取不正确: %+v", got)
	}
	open, ok, err := st.OpenActivity(ctx, model.FleetID)
	if err != nil || !ok || open.ID != act.ID {
		t.Fatalf("未结束活动查询失败: %+v %v", open, err)
	}
	byApp, ok, err := st.ActivityByApplication(ctx, app.ID)
	if err != nil || !ok || byApp.ID != act.ID {
		t.Fatalf("按申请查询活动失败: %v", err)
	}
	if err := st.UpdateActivity(ctx, act.ID, map[string]any{"status": model.ActStopped}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.OpenActivity(ctx, model.FleetID); ok {
		t.Fatal("已停止活动不应算作未结束")
	}
	if list, err := st.Activities(ctx); err != nil || len(list) != 1 {
		t.Fatalf("活动列表不正确: %v", err)
	}
	av := &model.ActivityVehicle{
		ActivityID: act.ID, VehicleID: "VEH-01", Phase: model.KindPilot,
		Dispatched: true, DispatchAt: "2026-11-13T10:01:00Z",
		TargetVersion: 1, TargetValue: 20, BaseValue: 60, Result: model.ResultPending,
	}
	if err := st.UpsertActivityVehicle(ctx, av); err != nil {
		t.Fatal(err)
	}
	av.Result = model.ResultSuccess
	av.NeedsRecovery = true
	if err := st.UpsertActivityVehicle(ctx, av); err != nil {
		t.Fatal(err)
	}
	one, ok, err := st.ActivityVehicle(ctx, act.ID, "VEH-01")
	if err != nil || !ok || one.Result != model.ResultSuccess || !one.NeedsRecovery {
		t.Fatalf("活动逐车记录不正确: %+v %v", one, err)
	}
	if err := st.UpdateActivityVehicle(ctx, act.ID, "VEH-01", map[string]any{"fail_reason": "应用失败"}); err != nil {
		t.Fatal(err)
	}
	one, _, _ = st.ActivityVehicle(ctx, act.ID, "VEH-01")
	if one.FailReason != "应用失败" {
		t.Fatalf("逐车更新未生效: %+v", one)
	}
	list, err := st.ActivityVehicles(ctx, act.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("逐车列表不正确: %v", err)
	}
}

func TestEmptyCollectionsAndNoopUpdate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	app := &model.Application{
		ID: "APP-000003", FleetID: model.FleetID, Reason: "空车辆列表", Interval: 30,
		Applicant: "dev", Status: model.AppPending,
		CreatedAt: "2026-11-13T10:00:00Z", UpdatedAt: "2026-11-13T10:00:00Z",
	}
	if err := st.InsertApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Application(ctx, app.ID)
	if err != nil || !ok || len(got.VehicleIDs) != 0 {
		t.Fatalf("空车辆列表应可读取: %+v %v", got, err)
	}
	act := &model.Activity{
		ID: "ACT-000009", ApplicationID: app.ID, FleetID: model.FleetID,
		Status: model.ActPiloting, Interval: 30, CreatedAt: "2026-11-13T10:00:00Z",
		CreatedBy: "ops", UpdatedAt: "2026-11-13T10:00:00Z",
	}
	if err := st.InsertActivity(ctx, act); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateActivityVehicle(ctx, act.ID, "VEH-01", nil); err != nil {
		t.Fatalf("空更新应直接返回: %v", err)
	}
	if err := st.UpdateApplication(ctx, app.ID, nil); err != nil {
		t.Fatalf("空更新应直接返回: %v", err)
	}
	if err := st.UpdateActivity(ctx, act.ID, nil); err != nil {
		t.Fatalf("空更新应直接返回: %v", err)
	}
}

// TestInitFailsOnUnwritablePath 覆盖初始化与查询的错误分支：
// 把 sqlite 文件指向一个已存在的目录，任何读写都会失败。
func TestInitFailsOnUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open("sqlite::@file(" + dir + ")")
	if err != nil {
		t.Fatalf("懒连接应允许打开成功: %v", err)
	}
	defer func() { _ = st.Close(context.Background()) }()
	ctx := context.Background()
	if err := st.Init(ctx); err == nil {
		t.Fatal("目录不可作为数据库文件，初始化应报错")
	}
	if _, err := st.Vehicles(ctx); err == nil {
		t.Fatal("查询车辆应报错")
	}
	if _, err := st.Applications(ctx); err == nil {
		t.Fatal("查询申请应报错")
	}
	if _, err := st.Activities(ctx); err == nil {
		t.Fatal("查询活动应报错")
	}
	if _, _, err := st.Activity(ctx, "ACT-1"); err == nil {
		t.Fatal("查询单个活动应报错")
	}
	if _, err := st.Receipts(ctx); err == nil {
		t.Fatal("查询回执应报错")
	}
	if _, err := st.InsertReceipt(ctx, &model.Receipt{VehicleID: "VEH-01", Version: 1}); err == nil {
		t.Fatal("写入回执应报错")
	}
	if _, err := st.NextSeq(ctx, "x"); err == nil {
		t.Fatal("取序列应报错")
	}
	if _, err := st.NextVersion(ctx); err == nil {
		t.Fatal("取版本应报错")
	}
}

func TestVersionAndReceiptRepositories(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	v1, err := st.NextVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := st.NextVersion(ctx)
	if err != nil || v2 != v1+1 {
		t.Fatalf("版本号应严格递增: %d %d", v1, v2)
	}
	if n, err := st.NextSeq(ctx, "custom"); err != nil || n != 1 {
		t.Fatalf("自定义序列应可用: %d %v", n, err)
	}
	pv := &model.PublishedVersion{
		VehicleID: "VEH-01", Version: v1, Interval: 30, Kind: model.KindPilot,
		ActivityID: "ACT-1", CreatedAt: "2026-11-13T10:00:00Z", CreatedBy: "ops",
	}
	if err := st.InsertConfigVersion(ctx, pv); err != nil {
		t.Fatal(err)
	}
	latest, ok, err := st.LatestConfigVersion(ctx, "VEH-01")
	if err != nil || !ok || latest.Version != v1 || latest.Interval != 30 {
		t.Fatalf("最新版本查询失败: %+v %v", latest, err)
	}
	if err := st.MarkDelivered(ctx, "VEH-01", v1); err != nil {
		t.Fatal(err)
	}
	latest, _, _ = st.LatestConfigVersion(ctx, "VEH-01")
	if !latest.Delivered {
		t.Fatal("下发标记未生效")
	}
	if list, err := st.ConfigVersionsOfVehicle(ctx, "VEH-01"); err != nil || len(list) != 1 {
		t.Fatalf("车辆版本列表不正确: %v", err)
	}
	if list, err := st.ConfigVersionsOfActivity(ctx, "ACT-1"); err != nil || len(list) != 1 {
		t.Fatalf("活动版本列表不正确: %v", err)
	}
	if owner, ok, err := st.ConfigVersionOwner(ctx, v1); err != nil || !ok || owner != "VEH-01" {
		t.Fatalf("版本归属查询失败: %s %v", owner, err)
	}
	if _, ok, _ := st.ConfigVersionOwner(ctx, 999); ok {
		t.Fatal("未知版本不应命中")
	}
	if _, ok, _ := st.LatestConfigVersion(ctx, "VEH-05"); ok {
		t.Fatal("未发布车辆不应有版本")
	}

	id, err := st.InsertReceipt(ctx, &model.Receipt{
		VehicleID: "VEH-01", Version: v1, Outcome: "success", Accepted: true,
		Recorded: true, At: "2026-11-13T10:05:00Z",
	})
	if err != nil || id == 0 {
		t.Fatalf("写入回执失败: %v", err)
	}
	if list, err := st.Receipts(ctx); err != nil || len(list) != 1 {
		t.Fatalf("回执列表不正确: %v", err)
	}
	if err := st.InsertConfigPull(ctx, &model.ConfigPull{
		VehicleID: "VEH-02", At: "2026-11-13T10:06:00Z", Granted: false, RejectReason: "未下发",
	}); err != nil {
		t.Fatal(err)
	}
	if list, err := st.ConfigPulls(ctx); err != nil || len(list) != 1 || list[0].Granted {
		t.Fatalf("拉取记录不正确: %v", err)
	}
	if err := st.InsertTimeoutEvent(ctx, &model.TimeoutEvent{
		ActivityID: "ACT-1", VehicleID: "VEH-01", Version: v1, At: "2026-11-13T10:07:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if list, err := st.TimeoutEventsOfVehicle(ctx, "VEH-01"); err != nil || len(list) != 1 {
		t.Fatalf("超时历史不正确: %v", err)
	}
	if err := st.ResolveTimeoutEvents(ctx, "VEH-01", v1); err != nil {
		t.Fatal(err)
	}
	if list, err := st.TimeoutEvents(ctx); err != nil || !list[0].ResolvedByLateSuccess {
		t.Fatalf("超时历史标记不正确: %v", err)
	}
	if err := st.InsertEvent(ctx, &model.Event{
		At: "2026-11-13T10:08:00Z", Actor: "ops", Role: "operator",
		Action: "activity.stop", Accepted: true, Reason: "停止活动",
	}); err != nil {
		t.Fatal(err)
	}
	if list, err := st.Events(ctx, 10); err != nil || len(list) != 1 {
		t.Fatalf("留痕列表不正确: %v", err)
	}
}
