// Package model 定义车队日志配置发布与恢复业务的领域模型、状态常量与业务错误。
package model

import "fmt"

// 固定业务口径（题面 §3）。
const (
	FleetID              = "FLEET-01"
	InitialInterval      = 60
	IntervalMin          = 10
	IntervalMax          = 300
	DefaultTimeoutSecond = 60
	PilotVehicleCount    = 2
)

// 用户角色。
type Role string

const (
	RoleApplicant Role = "applicant" // 研发申请人
	RoleApprover  Role = "approver"  // 审批负责人
	RoleOperator  Role = "operator"  // 发布运营
	RoleVehicle   Role = "vehicle"   // 模拟车辆
)

// User 会话身份（由 token 鉴权得出，禁止采信请求参数里的裸 ID）。
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Role      Role   `json:"role"`
	VehicleID string `json:"vehicle_id"`
}

func (u User) Is(r Role) bool { return u.Role == r }

// 申请状态机：draft -> pending -> approved / rejected（rejected 可修改后重新 pending）。
const (
	AppDraft    = "draft"
	AppPending  = "pending"
	AppApproved = "approved"
	AppRejected = "rejected"
)

// Activity 状态机：piloting -> promotion_ready -> promoting -> completed；
// piloting/promoting --失败或超时--> paused --停止--> stopped --批准恢复+下发--> recovering --确认--> recovered。
const (
	ActPiloting       = "piloting"
	ActPromotionReady = "promotion_ready"
	ActPromoting      = "promoting"
	ActPaused         = "paused"
	ActStopped        = "stopped"
	ActRecovering     = "recovering"
	ActRecovered      = "recovered"
	ActCompleted      = "completed"
)

// OpenStatuses 未结束活动状态（同车队同时只允许一个，R2）。
var OpenStatuses = map[string]bool{
	ActPiloting: true, ActPromotionReady: true, ActPromoting: true,
	ActPaused: true, ActRecovering: true,
}

// 下发阶段（版本 kind）。
const (
	KindPilot   = "pilot"
	KindPromote = "promote"
	KindRecover = "recover"
)

// 逐车结果。
const (
	ResultPending      = "pending"       // 已下发待回执
	ResultSuccess      = "success"       // 有效成功回执
	ResultFailure      = "failure"       // 失败（生效值不变）
	ResultTimeout      = "timeout"       // 超时（迟到成功可翻转）
	ResultNotPublished = "not_published" // 未下发
)

// Application 配置申请。批准后参数冻结（R1）。
type Application struct {
	ID           string   `json:"id" orm:"id"`
	FleetID      string   `json:"fleet_id" orm:"fleet_id"`
	Reason       string   `json:"reason" orm:"reason"`
	Interval     int      `json:"interval" orm:"interval_seconds"`
	VehicleIDs   []string `json:"vehicle_ids"`
	Applicant    string   `json:"applicant" orm:"applicant"`
	Approver     string   `json:"approver" orm:"approver"`
	Status       string   `json:"status" orm:"status"`
	RejectReason string   `json:"reject_reason" orm:"reject_reason"`
	CreatedAt    string   `json:"created_at" orm:"created_at"`
	UpdatedAt    string   `json:"updated_at" orm:"updated_at"`
	ApprovedAt   string   `json:"approved_at" orm:"approved_at"`
	Frozen       bool     `json:"frozen" orm:"frozen"`
}

// ApplicationInput 创建/修改申请的入参。
type ApplicationInput struct {
	Reason     string   `json:"reason" v:"required#请填写排查原因"`
	VehicleIDs []string `json:"vehicle_ids"`
	Interval   int      `json:"interval" v:"required"`
}

// Activity 发布活动。
type Activity struct {
	ID                string   `json:"id" orm:"id"`
	ApplicationID     string   `json:"application_id" orm:"application_id"`
	FleetID           string   `json:"fleet_id" orm:"fleet_id"`
	Status            string   `json:"status" orm:"status"`
	Interval          int      `json:"interval" orm:"interval_seconds"`
	PilotVehicleIDs   []string `json:"pilot_vehicle_ids"`
	TargetVehicleIDs  []string `json:"target_vehicle_ids"`
	CreatedBy         string   `json:"created_by" orm:"created_by"`
	CreatedAt         string   `json:"created_at" orm:"created_at"`
	UpdatedAt         string   `json:"updated_at" orm:"updated_at"`
	StoppedAt         string   `json:"stopped_at" orm:"stopped_at"`
	StoppedBy         string   `json:"stopped_by" orm:"stopped_by"`
	StopReason        string   `json:"stop_reason" orm:"stop_reason"`
	RestoreApprovedAt string   `json:"restore_approved_at" orm:"restore_approved_at"`
	RestoreApprovedBy string   `json:"restore_approved_by" orm:"restore_approved_by"`
	CompletedAt       string   `json:"completed_at" orm:"completed_at"`
	CompletedBy       string   `json:"completed_by" orm:"completed_by"`
	RecoveredAt       string   `json:"recovered_at" orm:"recovered_at"`
}

// ActivityVehicle 活动内逐车记录（发布前值快照、下发、结果）。
type ActivityVehicle struct {
	ActivityID    string `json:"activity_id" orm:"activity_id"`
	VehicleID     string `json:"vehicle_id" orm:"vehicle_id"`
	Phase         string `json:"phase" orm:"phase"`
	Dispatched    bool   `json:"dispatched" orm:"dispatched"`
	DispatchAt    string `json:"dispatch_at" orm:"dispatch_at"`
	TargetVersion int    `json:"target_version" orm:"target_version"`
	TargetValue   int    `json:"target_value" orm:"target_value"`
	BaseValue     int    `json:"base_value" orm:"base_value"`
	BaseVersion   int    `json:"base_version" orm:"base_version"`
	Result        string `json:"result" orm:"result"`
	FailReason    string `json:"fail_reason" orm:"fail_reason"`
	AckedAt       string `json:"acked_at" orm:"acked_at"`
	NeedsRecovery bool   `json:"needs_recovery" orm:"needs_recovery"`
}

// Vehicle 车辆档案。
type Vehicle struct {
	ID               string `json:"id" orm:"id"`
	FleetID          string `json:"fleet_id" orm:"fleet_id"`
	Online           bool   `json:"online" orm:"online"`
	BaselineValue    int    `json:"baseline_value" orm:"baseline_interval"`
	DesiredVersion   int    `json:"desired_version" orm:"desired_version"`
	DesiredValue     int    `json:"desired_value" orm:"desired_value"`
	EffectiveVersion int    `json:"effective_version" orm:"effective_version"`
	EffectiveValue   int    `json:"effective_value" orm:"effective_value"`
	Result           string `json:"result" orm:"result"`
	FailReason       string `json:"fail_reason" orm:"fail_reason"`
	ActivityID       string `json:"activity_id" orm:"activity_id"`
}

// VehicleState 车辆维度的期望值 / 生效值（R3：分别持久化，下发不改生效值）。
type VehicleState struct {
	VehicleID        string `json:"vehicle_id" orm:"vehicle_id"`
	BaselineValue    int    `json:"baseline_value" orm:"baseline_interval"`
	DesiredVersion   int    `json:"desired_version" orm:"desired_version"`
	DesiredValue     int    `json:"desired_value" orm:"desired_value"`
	EffectiveVersion int    `json:"effective_version" orm:"effective_version"`
	EffectiveValue   int    `json:"effective_value" orm:"effective_value"`
	Result           string `json:"result" orm:"result"`
	FailReason       string `json:"fail_reason" orm:"fail_reason"`
	ActivityID       string `json:"activity_id" orm:"activity_id"`
}

// PublishedVersion 面向某车的一次下发（期望值来源；下发 ≠ 生效）。
type PublishedVersion struct {
	VehicleID  string `json:"vehicle_id" orm:"vehicle_id"`
	Version    int    `json:"version" orm:"version"`
	Interval   int    `json:"interval" orm:"interval"`
	Kind       string `json:"kind" orm:"kind"`
	ActivityID string `json:"activity_id" orm:"activity_id"`
	CreatedAt  string `json:"created_at" orm:"created_at"`
	CreatedBy  string `json:"created_by" orm:"created_by"`
	Delivered  bool   `json:"delivered" orm:"delivered"`
}

// Receipt 车辆回执记录（accepted=false 表示拒绝留痕）。
type Receipt struct {
	ID           int64  `json:"id" orm:"id"`
	VehicleID    string `json:"vehicle_id" orm:"vehicle_id"`
	Version      int    `json:"version" orm:"version"`
	Outcome      string `json:"outcome" orm:"outcome"`
	Message      string `json:"message" orm:"message"`
	Accepted     bool   `json:"accepted" orm:"accepted"`
	Recorded     bool   `json:"recorded" orm:"recorded"`
	RejectReason string `json:"reject_reason" orm:"reject_reason"`
	ActivityID   string `json:"activity_id" orm:"activity_id"`
	At           string `json:"at" orm:"at"`
}

// ConfigPull 车辆拉取记录。
type ConfigPull struct {
	ID           int64  `json:"id" orm:"id"`
	VehicleID    string `json:"vehicle_id" orm:"vehicle_id"`
	Version      int    `json:"version" orm:"version"`
	At           string `json:"at" orm:"at"`
	Granted      bool   `json:"granted" orm:"granted"`
	RejectReason string `json:"reject_reason" orm:"reject_reason"`
}

// TimeoutEvent 超时历史（迟到成功只翻转结果，不清除本记录）。
type TimeoutEvent struct {
	ID                    int64  `json:"id" orm:"id"`
	ActivityID            string `json:"activity_id" orm:"activity_id"`
	VehicleID             string `json:"vehicle_id" orm:"vehicle_id"`
	Version               int    `json:"version" orm:"version"`
	At                    string `json:"at" orm:"at"`
	ResolvedByLateSuccess bool   `json:"resolved_by_late_success" orm:"resolved_by_late_success"`
}

// Event 越权/拒绝/关键动作留痕（谁、何时、什么操作、原因）。
type Event struct {
	ID         int64  `json:"id" orm:"id"`
	At         string `json:"at" orm:"at"`
	Actor      string `json:"actor" orm:"actor"`
	Role       string `json:"role" orm:"role"`
	Action     string `json:"action" orm:"action"`
	Target     string `json:"target" orm:"target"`
	Accepted   bool   `json:"accepted" orm:"accepted"`
	Reason     string `json:"reason" orm:"reason"`
	ActivityID string `json:"activity_id" orm:"activity_id"`
	VehicleID  string `json:"vehicle_id" orm:"vehicle_id"`
	Detail     string `json:"detail" orm:"detail"`
}

// PullResult 车辆拉取配置结果。
type PullResult struct {
	Granted      bool   `json:"granted"`
	RejectReason string `json:"reject_reason,omitempty"`
	VehicleID    string `json:"vehicle_id"`
	Version      int    `json:"version"`
	Interval     int    `json:"interval_seconds"`
	Kind         string `json:"kind"`
	ActivityID   string `json:"activity_id"`
	PublishedAt  string `json:"published_at"`
}

// ReceiptInput 车辆上报回执入参。VehicleID 仅用于演示跨车拒绝，身份以 token 为准。
type ReceiptInput struct {
	VehicleID string `json:"vehicle_id"`
	Version   int    `json:"version"`
	Success   bool   `json:"success"`
	Message   string `json:"message"`
}

// ReceiptResult 回执受理结果。
type ReceiptResult struct {
	Accepted         bool   `json:"accepted"`
	Recorded         bool   `json:"recorded"`
	Reason           string `json:"reason,omitempty"`
	EffectiveValue   int    `json:"effective_value"`
	EffectiveVersion int    `json:"effective_version"`
	Result           string `json:"result"`
}

// TimeoutEventDTO 逐车超时历史视图。
type TimeoutEventDTO struct {
	Version               int    `json:"version"`
	At                    string `json:"at"`
	ResolvedByLateSuccess bool   `json:"resolved_by_late_success"`
}

// VehicleProgress 逐车核查视图（发布前值/期望值/生效值/结果/原因，R7）。
type VehicleProgress struct {
	VehicleID         string             `json:"vehicle_id"`
	InActivity        bool               `json:"in_activity"`
	Phase             string             `json:"phase"`
	Dispatched        bool               `json:"dispatched"`
	DispatchAt        string             `json:"dispatch_at"`
	BaseValue         int                `json:"base_value"`
	BaseVersion       int                `json:"base_version"`
	ExpectedVersion   int                `json:"expected_version"`
	ExpectedValue     int                `json:"expected_value"`
	EffectiveVersion  int                `json:"effective_version"`
	EffectiveValue    int                `json:"effective_value"`
	Result            string             `json:"result"`
	FailReason        string             `json:"fail_reason"`
	AckedAt           string             `json:"acked_at"`
	NeedsRecovery     bool               `json:"needs_recovery"`
	TimeoutHistory    []TimeoutEventDTO  `json:"timeout_history"`
	PublishedVersions []PublishedVersion `json:"published_versions"`
}

// ActivityProgress 活动进度（含门禁判定与展示标志）。
type ActivityProgress struct {
	Activity                Activity          `json:"activity"`
	Application             Application       `json:"application"`
	Vehicles                []VehicleProgress `json:"vehicles"`
	PublishedVehicleIDs     []string          `json:"published_vehicle_ids"`
	UnpublishedVehicleIDs   []string          `json:"unpublished_vehicle_ids"`
	NeedsRecoveryVehicleIDs []string          `json:"needs_recovery_vehicle_ids"`
	PendingVehicleIDs       []string          `json:"pending_vehicle_ids"`
	FailedVehicleIDs        []string          `json:"failed_vehicle_ids"`
	SuccessCount            int               `json:"success_count"`
	AllConfirmed            bool              `json:"all_confirmed"`
	CanPromote              bool              `json:"can_promote"`
	PromoteBlockReason      string            `json:"promote_block_reason,omitempty"`
	CanComplete             bool              `json:"can_complete"`
	CompleteBlockReason     string            `json:"complete_block_reason,omitempty"`
	RestoreApproved         bool              `json:"restore_approved"`
	CanPublishRestore       bool              `json:"can_publish_restore"`
	RestoreBlockReason      string            `json:"restore_block_reason,omitempty"`
	RestoredCount           int               `json:"restored_count"`
	NeedsRecoveryCount      int               `json:"needs_recovery_count"`
	UnconfirmedRestoreIDs   []string          `json:"unconfirmed_restore_vehicle_ids"`
	AllRestored             bool              `json:"all_restored"`
	AllRecoveredShown       bool              `json:"all_recovered_shown"`
	TimeoutSeconds          int               `json:"timeout_seconds"`
}

// BizError 业务错误：带 HTTP 状态与稳定错误码，页面直接展示 Message。
type BizError struct {
	Status  int
	Code    string
	Message string
}

func (e *BizError) Error() string { return e.Message }

func NewErr(status int, code, format string, args ...any) *BizError {
	return &BizError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func ErrBadRequest(code, format string, args ...any) *BizError {
	return NewErr(400, code, format, args...)
}
func ErrUnauthorized(format string, args ...any) *BizError {
	return NewErr(401, "UNAUTHORIZED", format, args...)
}
func ErrForbidden(code, format string, args ...any) *BizError {
	return NewErr(403, code, format, args...)
}
func ErrNotFound(format string, args ...any) *BizError {
	return NewErr(404, "NOT_FOUND", format, args...)
}
func ErrConflict(code, format string, args ...any) *BizError {
	return NewErr(409, code, format, args...)
}
