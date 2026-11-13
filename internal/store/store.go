// Package store 基于 GoFrame gdb + 纯 Go SQLite 驱动实现持久化存储。
// 数据库选型说明：SQLite 单文件真实数据库，服务重启后业务结果仍可查询（V21）；
// 驱动 github.com/gogf/gf/contrib/drivers/sqlite（glebarez/go-sqlite，无需 cgo）。
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	_ "github.com/gogf/gf/contrib/drivers/sqlite/v2" // 注册 sqlite 驱动
	"github.com/gogf/gf/v2/database/gdb"

	"fleet/internal/model"
)

// Store 持久化存储门面。
type Store struct {
	db gdb.DB
}

// Open 打开数据库连接（link 形如 sqlite::@file(./data/fleet.db)）。
func Open(link string) (*Store, error) {
	db, err := gdb.New(gdb.ConfigNode{
		Type: "sqlite", Link: link, Charset: "utf8",
		// 时间字段由业务显式写入，关闭 gdb 自动维护，避免把未提供的 updated_at 置空
		TimeMaintainDisabled: true,
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConnCount(1) // SQLite 单写者，配合业务互斥锁避免锁竞争
	db.SetMaxIdleConnCount(1)
	return &Store{db: db}, nil
}

// DB 返回底层 gdb.DB（测试与导出使用）。
func (s *Store) DB() gdb.DB { return s.db }

// Close 关闭连接。
func (s *Store) Close(ctx context.Context) error { return s.db.Close(ctx) }

var ddlStatements = []string{
	`CREATE TABLE IF NOT EXISTS seq (
		name  TEXT PRIMARY KEY,
		value INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS users (
		id            TEXT PRIMARY KEY,
		username      TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		token         TEXT UNIQUE NOT NULL,
		role          TEXT NOT NULL,
		name          TEXT NOT NULL,
		vehicle_id    TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS vehicles (
		id               TEXT PRIMARY KEY,
		fleet_id         TEXT NOT NULL,
		online           INTEGER NOT NULL DEFAULT 1,
		baseline_interval INTEGER NOT NULL DEFAULT 60
	)`,
	`CREATE TABLE IF NOT EXISTS vehicle_states (
		vehicle_id        TEXT PRIMARY KEY,
		baseline_interval INTEGER NOT NULL DEFAULT 60,
		desired_version   INTEGER NOT NULL DEFAULT 0,
		desired_value     INTEGER NOT NULL DEFAULT 0,
		effective_version INTEGER NOT NULL DEFAULT 0,
		effective_value   INTEGER NOT NULL DEFAULT 60,
		result            TEXT    NOT NULL DEFAULT 'not_published',
		fail_reason       TEXT    NOT NULL DEFAULT '',
		activity_id       TEXT    NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS applications (
		id              TEXT PRIMARY KEY,
		fleet_id        TEXT NOT NULL,
		reason          TEXT NOT NULL,
		interval_seconds INTEGER NOT NULL,
		vehicle_ids     TEXT NOT NULL,
		applicant       TEXT NOT NULL,
		approver        TEXT NOT NULL DEFAULT '',
		status          TEXT NOT NULL,
		reject_reason   TEXT NOT NULL DEFAULT '',
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		approved_at     TEXT NOT NULL DEFAULT '',
		frozen          INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS activities (
		id                   TEXT PRIMARY KEY,
		application_id       TEXT NOT NULL,
		fleet_id             TEXT NOT NULL,
		status               TEXT NOT NULL,
		interval_seconds     INTEGER NOT NULL,
		pilot_vehicle_ids    TEXT NOT NULL,
		target_vehicle_ids   TEXT NOT NULL,
		created_by           TEXT NOT NULL,
		created_at           TEXT NOT NULL,
		updated_at           TEXT NOT NULL,
		stopped_at           TEXT NOT NULL DEFAULT '',
		stopped_by           TEXT NOT NULL DEFAULT '',
		stop_reason          TEXT NOT NULL DEFAULT '',
		restore_approved_at  TEXT NOT NULL DEFAULT '',
		restore_approved_by  TEXT NOT NULL DEFAULT '',
		completed_at         TEXT NOT NULL DEFAULT '',
		completed_by         TEXT NOT NULL DEFAULT '',
		recovered_at         TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS activity_vehicles (
		activity_id    TEXT NOT NULL,
		vehicle_id     TEXT NOT NULL,
		phase          TEXT NOT NULL DEFAULT '',
		dispatched     INTEGER NOT NULL DEFAULT 0,
		dispatch_at    TEXT NOT NULL DEFAULT '',
		target_version INTEGER NOT NULL DEFAULT 0,
		target_value   INTEGER NOT NULL DEFAULT 0,
		base_value     INTEGER NOT NULL DEFAULT 60,
		base_version   INTEGER NOT NULL DEFAULT 0,
		result         TEXT NOT NULL DEFAULT 'not_published',
		fail_reason    TEXT NOT NULL DEFAULT '',
		acked_at       TEXT NOT NULL DEFAULT '',
		needs_recovery INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (activity_id, vehicle_id)
	)`,
	`CREATE TABLE IF NOT EXISTS config_versions (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		vehicle_id       TEXT NOT NULL,
		version          INTEGER NOT NULL,
		interval_seconds INTEGER NOT NULL,
		kind             TEXT NOT NULL,
		activity_id      TEXT NOT NULL,
		created_at       TEXT NOT NULL,
		created_by       TEXT NOT NULL,
		delivered        INTEGER NOT NULL DEFAULT 0,
		UNIQUE (vehicle_id, version)
	)`,
	`CREATE TABLE IF NOT EXISTS receipts (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		vehicle_id    TEXT NOT NULL,
		version       INTEGER NOT NULL,
		outcome       TEXT NOT NULL,
		message       TEXT NOT NULL DEFAULT '',
		accepted      INTEGER NOT NULL DEFAULT 0,
		recorded      INTEGER NOT NULL DEFAULT 0,
		reject_reason TEXT NOT NULL DEFAULT '',
		activity_id   TEXT NOT NULL DEFAULT '',
		at            TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS config_pulls (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		vehicle_id    TEXT NOT NULL,
		version       INTEGER NOT NULL DEFAULT 0,
		at            TEXT NOT NULL,
		granted       INTEGER NOT NULL DEFAULT 0,
		reject_reason TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS timeout_events (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
		activity_id               TEXT NOT NULL,
		vehicle_id                TEXT NOT NULL,
		version                   INTEGER NOT NULL,
		at                        TEXT NOT NULL,
		resolved_by_late_success  INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS events (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		at          TEXT NOT NULL,
		actor       TEXT NOT NULL,
		role        TEXT NOT NULL,
		action      TEXT NOT NULL,
		target      TEXT NOT NULL DEFAULT '',
		accepted    INTEGER NOT NULL DEFAULT 1,
		reason      TEXT NOT NULL DEFAULT '',
		activity_id TEXT NOT NULL DEFAULT '',
		vehicle_id  TEXT NOT NULL DEFAULT '',
		detail      TEXT NOT NULL DEFAULT ''
	)`,
}

// Init 建表、预置数据与序列，全部幂等（重复启动不重复写入）。
func (s *Store) Init(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("设置 WAL: %w", err)
	}
	if _, err := s.db.Exec(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("设置 busy_timeout: %w", err)
	}
	for _, ddl := range ddlStatements {
		if _, err := s.db.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("初始化表结构: %w", err)
		}
	}
	if err := s.seedSeq(ctx); err != nil {
		return err
	}
	if err := s.SeedVehicles(ctx); err != nil {
		return err
	}
	if err := s.SeedUsers(ctx); err != nil {
		return err
	}
	return s.ensureVehicleStates(ctx)
}

func (s *Store) seedSeq(ctx context.Context) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO seq(name, value) VALUES('vehicle_version', 0)
		 ON CONFLICT(name) DO NOTHING`)
	return err
}

// presetVehicles 五台预置车辆：VEH-01 在线试点车，VEH-02 离线试点车（网络分支）。
func PresetVehicles() []model.Vehicle {
	online := map[string]bool{"VEH-01": true, "VEH-03": true, "VEH-04": true, "VEH-05": true}
	ids := []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}
	list := make([]model.Vehicle, 0, len(ids))
	for _, id := range ids {
		list = append(list, model.Vehicle{
			ID: id, FleetID: model.FleetID, Online: online[id], BaselineValue: model.InitialInterval,
		})
	}
	return list
}

// SeedVehicles 预置车队 FLEET-01 与五台车辆（幂等）。
func (s *Store) SeedVehicles(ctx context.Context) error {
	for _, v := range PresetVehicles() {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO vehicles(id, fleet_id, online, baseline_interval)
			 VALUES(?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			v.ID, v.FleetID, v.Online, v.BaselineValue); err != nil {
			return err
		}
	}
	return nil
}

// PresetUsers 预置账号：研发申请人 / 审批负责人 / 发布运营 / 模拟车辆。
func PresetUsers() []model.User {
	users := []model.User{
		{ID: "dev", Username: "dev", Name: "研发申请人(dev)", Role: model.RoleApplicant},
		{ID: "approver", Username: "approver", Name: "审批负责人(approver)", Role: model.RoleApprover},
		{ID: "ops", Username: "ops", Name: "发布运营(ops)", Role: model.RoleOperator},
	}
	for i := 1; i <= 5; i++ {
		vid := fmt.Sprintf("VEH-%02d", i)
		users = append(users, model.User{
			ID: fmt.Sprintf("vehicle-%02d", i), Username: fmt.Sprintf("vehicle-%02d", i),
			Name: "模拟车辆(" + vid + ")", Role: model.RoleVehicle, VehicleID: vid,
		})
	}
	return users
}

// PresetPassword 演示账号统一密码（教学环境预置，非生产密钥）。
const PresetPassword = "fleet123456"

// TokenFor 计算账号预置 token（由用户名派生，避免明文入库敏感信息）。
func TokenFor(username string) string {
	sum := sha256.Sum256([]byte("fleet-token:" + username))
	return "tok-" + hex.EncodeToString(sum[:])[:24]
}

// HashPassword 计算口令哈希。
func HashPassword(password string) string {
	sum := sha256.Sum256([]byte("fleet-pwd:" + password))
	return hex.EncodeToString(sum[:])
}

// SeedUsers 预置四类角色账号（幂等）。
func (s *Store) SeedUsers(ctx context.Context) error {
	for _, u := range PresetUsers() {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO users(id, username, password_hash, token, role, name, vehicle_id)
			 VALUES(?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			u.ID, u.Username, HashPassword(PresetPassword), TokenFor(u.Username),
			string(u.Role), u.Name, u.VehicleID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureVehicleStates(ctx context.Context) error {
	for _, v := range PresetVehicles() {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO vehicle_states(vehicle_id, baseline_interval, effective_value, result)
			 VALUES(?, ?, ?, 'not_published') ON CONFLICT(vehicle_id) DO NOTHING`,
			v.ID, v.BaselineValue, v.BaselineValue); err != nil {
			return err
		}
	}
	return nil
}
