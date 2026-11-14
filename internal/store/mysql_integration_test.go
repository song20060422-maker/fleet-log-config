package store_test

import (
	"context"
	"os"
	"testing"

	"fleet/internal/model"
	"fleet/internal/store"
)

// TestMySQLInitSeedAndUpsert 验证 MySQL 方言下的建表、预置与幂等写入。
// 需要提供库（CI 由 mysql 服务提供）：
//
//	FLEET_TEST_MYSQL_LINK=mysql:root:root@tcp(127.0.0.1:3306)/fleet_test?charset=utf8mb4&parseTime=True&loc=Local
func TestMySQLInitSeedAndUpsert(t *testing.T) {
	link := os.Getenv("FLEET_TEST_MYSQL_LINK")
	if link == "" {
		t.Skip("未配置 FLEET_TEST_MYSQL_LINK，跳过 MySQL 集成用例")
	}
	ctx := context.Background()
	st, err := store.Open(link)
	if err != nil {
		t.Fatalf("连接 MySQL 失败: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	if st.Dialect() != store.DialectMySQL {
		t.Fatalf("方言应为 mysql, 实际 %s", st.Dialect())
	}
	if err := st.Init(ctx); err != nil {
		t.Fatalf("MySQL 建表/预置失败: %v", err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatalf("重复初始化应幂等: %v", err)
	}
	vehicles, err := st.Vehicles(ctx)
	if err != nil || len(vehicles) != 5 {
		t.Fatalf("预置车辆应为 5 台: %v %v", len(vehicles), err)
	}
	if _, ok, err := st.UserByCredentials(ctx, "dev", store.HashPassword(store.PresetPassword)); err != nil || !ok {
		t.Fatalf("预置账号应可登录: %v %v", ok, err)
	}

	av := &model.ActivityVehicle{
		ActivityID: "ACT-IT-1", VehicleID: "VEH-01", Phase: "pilot", Dispatched: true,
		TargetVersion: 1, TargetValue: 30, BaseValue: 60, Result: model.ResultPending,
	}
	if err := st.UpsertActivityVehicle(ctx, av); err != nil {
		t.Fatalf("写入逐车记录失败: %v", err)
	}
	av.Result = model.ResultSuccess
	av.NeedsRecovery = true
	if err := st.UpsertActivityVehicle(ctx, av); err != nil {
		t.Fatalf("更新逐车记录失败: %v", err)
	}
	list, err := st.ActivityVehicles(ctx, av.ActivityID)
	if err != nil || len(list) != 1 {
		t.Fatalf("upsert 应保持单条: %v %v", len(list), err)
	}
	if list[0].Result != model.ResultSuccess || !list[0].NeedsRecovery {
		t.Fatalf("upsert 未更新字段: %+v", list[0])
	}

	v1, err := st.NextVersion(ctx)
	if err != nil {
		t.Fatalf("取版本号失败: %v", err)
	}
	v2, err := st.NextVersion(ctx)
	if err != nil || v2 <= v1 {
		t.Fatalf("版本号应递增: %d -> %d (%v)", v1, v2, err)
	}

	if _, err := st.DB().Exec(ctx, `DELETE FROM activity_vehicles WHERE activity_id=?`, av.ActivityID); err != nil {
		t.Fatalf("清理逐车记录失败: %v", err)
	}
}
