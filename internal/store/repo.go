package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"

	"fleet/internal/model"
)

func NowString() string { return time.Now().UTC().Format(time.RFC3339) }

func joinIDs(ids []string) string { return strings.Join(ids, ",") }

func splitIDs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// queryOne 查询单行并扫描到 dest，返回是否存在。
func (s *Store) queryOne(ctx context.Context, dest any, sql string, args ...any) (bool, error) {
	rec, err := s.db.GetOne(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	if rec.IsEmpty() {
		return false, nil
	}
	return true, rec.Struct(dest)
}

// queryList 查询多行并扫描到 dest（切片指针）。
func (s *Store) queryList(ctx context.Context, dest any, sql string, args ...any) error {
	res, err := s.db.GetAll(ctx, sql, args...)
	if err != nil {
		return err
	}
	return res.Structs(dest)
}

func (s *Store) updateRow(ctx context.Context, table, key, val string, data map[string]any) error {
	if len(data) == 0 {
		return nil
	}
	_, err := s.db.Update(ctx, table, data, fmt.Sprintf("%s=?", key), val)
	return err
}

// ---------------------------------------------------------------- 用户与鉴权

func (s *Store) UserByToken(ctx context.Context, token string) (model.User, bool, error) {
	var u model.User
	ok, err := s.queryOne(ctx, &u,
		`SELECT id, username, role, name, vehicle_id FROM users WHERE token=?`, token)
	return u, ok, err
}

func (s *Store) UserByCredentials(ctx context.Context, username, passwordHash string) (model.User, bool, error) {
	var u model.User
	ok, err := s.queryOne(ctx, &u,
		`SELECT id, username, role, name, vehicle_id FROM users WHERE username=? AND password_hash=?`,
		username, passwordHash)
	return u, ok, err
}

// ---------------------------------------------------------------- 车辆

func (s *Store) Vehicles(ctx context.Context) ([]model.Vehicle, error) {
	var list []model.Vehicle
	err := s.queryList(ctx, &list, `SELECT * FROM vehicles ORDER BY id`)
	return list, err
}

func (s *Store) Vehicle(ctx context.Context, id string) (model.Vehicle, bool, error) {
	var v model.Vehicle
	ok, err := s.queryOne(ctx, &v, `SELECT * FROM vehicles WHERE id=?`, id)
	return v, ok, err
}

// ---------------------------------------------------------------- 车辆状态（期望值/生效值）

func (s *Store) VehicleState(ctx context.Context, vehicleID string) (model.VehicleState, bool, error) {
	var v model.VehicleState
	ok, err := s.queryOne(ctx, &v, `SELECT * FROM vehicle_states WHERE vehicle_id=?`, vehicleID)
	return v, ok, err
}

func (s *Store) VehicleStates(ctx context.Context) ([]model.VehicleState, error) {
	var list []model.VehicleState
	err := s.queryList(ctx, &list, `SELECT * FROM vehicle_states ORDER BY vehicle_id`)
	return list, err
}

func (s *Store) UpdateVehicleState(ctx context.Context, vehicleID string, data map[string]any) error {
	return s.updateRow(ctx, "vehicle_states", "vehicle_id", vehicleID, data)
}

// ---------------------------------------------------------------- 申请

func (s *Store) InsertApplication(ctx context.Context, a *model.Application) error {
	_, err := s.db.Insert(ctx, "applications", g.Map{
		"id": a.ID, "fleet_id": a.FleetID, "reason": a.Reason,
		"interval_seconds": a.Interval, "vehicle_ids": joinIDs(a.VehicleIDs),
		"applicant": a.Applicant, "approver": a.Approver, "status": a.Status,
		"reject_reason": a.RejectReason, "created_at": a.CreatedAt,
		"updated_at": a.UpdatedAt, "approved_at": a.ApprovedAt, "frozen": a.Frozen,
	})
	return err
}

func (s *Store) UpdateApplication(ctx context.Context, id string, data map[string]any) error {
	return s.updateRow(ctx, "applications", "id", id, data)
}

func scanApplication(rec gdb.Record) (model.Application, error) {
	var a model.Application
	if err := rec.Struct(&a); err != nil {
		return a, err
	}
	a.VehicleIDs = splitIDs(rec["vehicle_ids"].String())
	return a, nil
}

func (s *Store) Application(ctx context.Context, id string) (model.Application, bool, error) {
	rec, err := s.db.GetOne(ctx, `SELECT * FROM applications WHERE id=?`, id)
	if err != nil || rec.IsEmpty() {
		return model.Application{}, false, err
	}
	a, err := scanApplication(rec)
	return a, true, err
}

func (s *Store) Applications(ctx context.Context) ([]model.Application, error) {
	res, err := s.db.GetAll(ctx, `SELECT * FROM applications ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	list := make([]model.Application, 0, len(res))
	for _, rec := range res {
		a, err := scanApplication(rec)
		if err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, nil
}

// ---------------------------------------------------------------- 活动

func (s *Store) InsertActivity(ctx context.Context, a *model.Activity) error {
	_, err := s.db.Insert(ctx, "activities", g.Map{
		"id": a.ID, "application_id": a.ApplicationID, "fleet_id": a.FleetID,
		"status": a.Status, "interval_seconds": a.Interval,
		"pilot_vehicle_ids":  joinIDs(a.PilotVehicleIDs),
		"target_vehicle_ids": joinIDs(a.TargetVehicleIDs),
		"created_by":         a.CreatedBy, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt,
	})
	return err
}

func (s *Store) UpdateActivity(ctx context.Context, id string, data map[string]any) error {
	return s.updateRow(ctx, "activities", "id", id, data)
}

func scanActivity(rec gdb.Record) (model.Activity, error) {
	var a model.Activity
	if err := rec.Struct(&a); err != nil {
		return a, err
	}
	a.PilotVehicleIDs = splitIDs(rec["pilot_vehicle_ids"].String())
	a.TargetVehicleIDs = splitIDs(rec["target_vehicle_ids"].String())
	return a, nil
}

func (s *Store) Activity(ctx context.Context, id string) (model.Activity, bool, error) {
	rec, err := s.db.GetOne(ctx, `SELECT * FROM activities WHERE id=?`, id)
	if err != nil || rec.IsEmpty() {
		return model.Activity{}, false, err
	}
	a, err := scanActivity(rec)
	return a, true, err
}

func (s *Store) Activities(ctx context.Context) ([]model.Activity, error) {
	res, err := s.db.GetAll(ctx, `SELECT * FROM activities ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	list := make([]model.Activity, 0, len(res))
	for _, rec := range res {
		a, err := scanActivity(rec)
		if err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, nil
}

// ActivityByApplication 查询某申请已创建的活动（一个申请只允许一个活动）。
func (s *Store) ActivityByApplication(ctx context.Context, applicationID string) (model.Activity, bool, error) {
	rec, err := s.db.GetOne(ctx,
		`SELECT * FROM activities WHERE application_id=? ORDER BY created_at DESC LIMIT 1`, applicationID)
	if err != nil || rec.IsEmpty() {
		return model.Activity{}, false, err
	}
	a, err := scanActivity(rec)
	return a, true, err
}

// OpenActivity 查询车队当前未结束活动（同车队单一活动约束 R2）。
func (s *Store) OpenActivity(ctx context.Context, fleetID string) (model.Activity, bool, error) {
	rec, err := s.db.GetOne(ctx,
		`SELECT * FROM activities WHERE fleet_id=? AND status IN ('piloting','promotion_ready','promoting','paused','recovering')
		 ORDER BY created_at DESC LIMIT 1`, fleetID)
	if err != nil || rec.IsEmpty() {
		return model.Activity{}, false, err
	}
	a, err := scanActivity(rec)
	return a, true, err
}

// ---------------------------------------------------------------- 活动逐车记录

// UpsertActivityVehicle 写入或更新活动逐车记录（幂等，按方言选择冲突处理语法）。
func (s *Store) UpsertActivityVehicle(ctx context.Context, av *model.ActivityVehicle) error {
	const columns = `activity_vehicles(activity_id, vehicle_id, phase, dispatched, dispatch_at,
			target_version, target_value, base_value, base_version, result, fail_reason, acked_at, needs_recovery)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	var stmt string
	if s.dialect == DialectMySQL {
		stmt = `INSERT INTO ` + columns + ` ON DUPLICATE KEY UPDATE
			phase=VALUES(phase), dispatched=VALUES(dispatched), dispatch_at=VALUES(dispatch_at),
			target_version=VALUES(target_version), target_value=VALUES(target_value),
			result=VALUES(result), fail_reason=VALUES(fail_reason), acked_at=VALUES(acked_at),
			needs_recovery=VALUES(needs_recovery)`
	} else {
		stmt = `INSERT INTO ` + columns + ` ON CONFLICT(activity_id, vehicle_id) DO UPDATE SET
			phase=excluded.phase, dispatched=excluded.dispatched, dispatch_at=excluded.dispatch_at,
			target_version=excluded.target_version, target_value=excluded.target_value,
			result=excluded.result, fail_reason=excluded.fail_reason, acked_at=excluded.acked_at,
			needs_recovery=excluded.needs_recovery`
	}
	_, err := s.db.Exec(ctx, stmt,
		av.ActivityID, av.VehicleID, av.Phase, av.Dispatched, av.DispatchAt,
		av.TargetVersion, av.TargetValue, av.BaseValue, av.BaseVersion,
		av.Result, av.FailReason, av.AckedAt, av.NeedsRecovery)
	return err
}

func (s *Store) UpdateActivityVehicle(ctx context.Context, activityID, vehicleID string, data map[string]any) error {
	if len(data) == 0 {
		return nil
	}
	_, err := s.db.Update(ctx, "activity_vehicles", data,
		"activity_id=? AND vehicle_id=?", activityID, vehicleID)
	return err
}

func (s *Store) ActivityVehicle(ctx context.Context, activityID, vehicleID string) (model.ActivityVehicle, bool, error) {
	var av model.ActivityVehicle
	ok, err := s.queryOne(ctx, &av,
		`SELECT * FROM activity_vehicles WHERE activity_id=? AND vehicle_id=?`, activityID, vehicleID)
	return av, ok, err
}

func (s *Store) ActivityVehicles(ctx context.Context, activityID string) ([]model.ActivityVehicle, error) {
	var list []model.ActivityVehicle
	err := s.queryList(ctx, &list,
		`SELECT * FROM activity_vehicles WHERE activity_id=? ORDER BY vehicle_id`, activityID)
	return list, err
}

// ---------------------------------------------------------------- 发布版本（期望值来源）

// NextSeq 取指定序列的下一个值（原子递增，行不存在则先建为 0 再加 1）。
func (s *Store) NextSeq(ctx context.Context, name string) (int, error) {
	if _, err := s.db.Exec(ctx,
		s.insertSQL(`seq(name, value) VALUES(?, 0)`, ` ON CONFLICT(name) DO NOTHING`), name); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE seq SET value=value+1 WHERE name=?`, name); err != nil {
		return 0, err
	}
	val, err := s.db.GetValue(ctx, `SELECT value FROM seq WHERE name=?`, name)
	if err != nil {
		return 0, err
	}
	return val.Int(), nil
}

// NextVersion 取下一车辆配置版本号。版本按车辆记录严格递增，同时全局唯一，
// 以便试点/推广/恢复版本可区分（见需求假设 A1 补充说明）。
func (s *Store) NextVersion(ctx context.Context) (int, error) {
	return s.NextSeq(ctx, "vehicle_version")
}

func (s *Store) InsertConfigVersion(ctx context.Context, pv *model.PublishedVersion) error {
	_, err := s.db.Insert(ctx, "config_versions", g.Map{
		"vehicle_id": pv.VehicleID, "version": pv.Version,
		"interval_seconds": pv.Interval, "kind": pv.Kind, "activity_id": pv.ActivityID,
		"created_at": pv.CreatedAt, "created_by": pv.CreatedBy, "delivered": pv.Delivered,
	})
	return err
}

func (s *Store) MarkDelivered(ctx context.Context, vehicleID string, version int) error {
	_, err := s.db.Update(ctx, "config_versions", g.Map{"delivered": 1},
		"vehicle_id=? AND version=?", vehicleID, version)
	return err
}

func (s *Store) LatestConfigVersion(ctx context.Context, vehicleID string) (model.PublishedVersion, bool, error) {
	var pv model.PublishedVersion
	ok, err := s.queryOne(ctx, &pv,
		`SELECT vehicle_id, version, interval_seconds AS `+"`interval`"+`, kind, activity_id, created_at, created_by, delivered
		 FROM config_versions WHERE vehicle_id=? ORDER BY version DESC LIMIT 1`, vehicleID)
	return pv, ok, err
}

func (s *Store) ConfigVersionsOfVehicle(ctx context.Context, vehicleID string) ([]model.PublishedVersion, error) {
	var list []model.PublishedVersion
	err := s.queryList(ctx, &list,
		`SELECT vehicle_id, version, interval_seconds AS `+"`interval`"+`, kind, activity_id, created_at, created_by, delivered
		 FROM config_versions WHERE vehicle_id=? ORDER BY version`, vehicleID)
	return list, err
}

// ConfigVersionsOfActivity 查询某活动产生的全部下发版本。
func (s *Store) ConfigVersionsOfActivity(ctx context.Context, activityID string) ([]model.PublishedVersion, error) {
	var list []model.PublishedVersion
	err := s.queryList(ctx, &list,
		`SELECT vehicle_id, version, interval_seconds AS `+"`interval`"+`, kind, activity_id, created_at, created_by, delivered
		 FROM config_versions WHERE activity_id=? ORDER BY version`, activityID)
	return list, err
}

// ConfigVersionOwner 查询某版本面向哪台车发布（用于跨车回执识别）。
func (s *Store) ConfigVersionOwner(ctx context.Context, version int) (string, bool, error) {
	val, err := s.db.GetValue(ctx,
		`SELECT vehicle_id FROM config_versions WHERE version=? ORDER BY id LIMIT 1`, version)
	if err != nil || val.IsEmpty() {
		return "", false, err
	}
	return val.String(), true, nil
}

// ---------------------------------------------------------------- 回执

func (s *Store) InsertReceipt(ctx context.Context, r *model.Receipt) (int64, error) {
	res, err := s.db.Insert(ctx, "receipts", g.Map{
		"vehicle_id": r.VehicleID, "version": r.Version, "outcome": r.Outcome,
		"message": r.Message, "accepted": r.Accepted, "recorded": r.Recorded,
		"reject_reason": r.RejectReason, "activity_id": r.ActivityID, "at": r.At,
	})
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *Store) Receipts(ctx context.Context) ([]model.Receipt, error) {
	var list []model.Receipt
	err := s.queryList(ctx, &list, `SELECT * FROM receipts ORDER BY id`)
	return list, err
}

// ---------------------------------------------------------------- 拉取记录

func (s *Store) InsertConfigPull(ctx context.Context, p *model.ConfigPull) error {
	_, err := s.db.Insert(ctx, "config_pulls", g.Map{
		"vehicle_id": p.VehicleID, "version": p.Version, "at": p.At,
		"granted": p.Granted, "reject_reason": p.RejectReason,
	})
	return err
}

func (s *Store) ConfigPulls(ctx context.Context) ([]model.ConfigPull, error) {
	var list []model.ConfigPull
	err := s.queryList(ctx, &list, `SELECT * FROM config_pulls ORDER BY id`)
	return list, err
}

// ConfigPullsOfVehicle 查询某车的拉取记录（最新在前）。
func (s *Store) ConfigPullsOfVehicle(ctx context.Context, vehicleID string) ([]model.ConfigPull, error) {
	var list []model.ConfigPull
	err := s.queryList(ctx, &list,
		`SELECT * FROM config_pulls WHERE vehicle_id=? ORDER BY id DESC`, vehicleID)
	return list, err
}

// ---------------------------------------------------------------- 超时历史

func (s *Store) InsertTimeoutEvent(ctx context.Context, te *model.TimeoutEvent) error {
	_, err := s.db.Insert(ctx, "timeout_events", g.Map{
		"activity_id": te.ActivityID, "vehicle_id": te.VehicleID,
		"version": te.Version, "at": te.At,
		"resolved_by_late_success": te.ResolvedByLateSuccess,
	})
	return err
}

func (s *Store) ResolveTimeoutEvents(ctx context.Context, vehicleID string, version int) error {
	_, err := s.db.Update(ctx, "timeout_events", g.Map{"resolved_by_late_success": 1},
		"vehicle_id=? AND version=?", vehicleID, version)
	return err
}

func (s *Store) TimeoutEvents(ctx context.Context) ([]model.TimeoutEvent, error) {
	var list []model.TimeoutEvent
	err := s.queryList(ctx, &list, `SELECT * FROM timeout_events ORDER BY id`)
	return list, err
}

func (s *Store) TimeoutEventsOfVehicle(ctx context.Context, vehicleID string) ([]model.TimeoutEvent, error) {
	var list []model.TimeoutEvent
	err := s.queryList(ctx, &list,
		`SELECT * FROM timeout_events WHERE vehicle_id=? ORDER BY id`, vehicleID)
	return list, err
}

// ---------------------------------------------------------------- 留痕事件

func (s *Store) InsertEvent(ctx context.Context, e *model.Event) error {
	_, err := s.db.Insert(ctx, "events", g.Map{
		"at": e.At, "actor": e.Actor, "role": e.Role, "action": e.Action,
		"target": e.Target, "accepted": e.Accepted, "reason": e.Reason,
		"activity_id": e.ActivityID, "vehicle_id": e.VehicleID, "detail": e.Detail,
	})
	return err
}

func (s *Store) Events(ctx context.Context, limit int) ([]model.Event, error) {
	var list []model.Event
	err := s.queryList(ctx, &list,
		fmt.Sprintf(`SELECT * FROM events ORDER BY id DESC LIMIT %d`, limit))
	return list, err
}
