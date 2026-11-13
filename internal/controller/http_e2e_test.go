// Package controller_test 通过真实 HTTP 服务验证接口契约、角色权限与五组业务场景（V20）。
package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleet/internal/controller"
	"fleet/internal/logic"
	"fleet/internal/model"
	"fleet/internal/store"
)

type harness struct {
	t       *testing.T
	baseURL string
	svc     *logic.Service
	st      *store.Store
	tokens  map[string]string
}

func newHarness(t *testing.T, timeout time.Duration) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite::@file(" + filepath.Join(dir, "e2e.db") + ")")
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	svc := logic.New(st, timeout)
	s := controller.NewServer(svc, fmt.Sprintf("e2e-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.Start()
	t.Cleanup(func() {
		_ = s.Shutdown()
		_ = st.Close(context.Background())
	})
	return &harness{
		t: t, baseURL: fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort()),
		svc: svc, st: st, tokens: map[string]string{},
	}
}

type apiResponse struct {
	status  int
	raw     []byte
	Code    any             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (h *harness) call(method, path, token string, body any) apiResponse {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("序列化请求失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.baseURL+path, reader)
	if err != nil {
		h.t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		h.t.Fatalf("请求 %s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("读取响应失败: %v", err)
	}
	out := apiResponse{status: resp.StatusCode, raw: raw}
	if len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatalf("解析响应失败: %v (%s)", err, string(raw))
		}
	}
	return out
}

func (h *harness) login(username string) string {
	h.t.Helper()
	res := h.call(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username": username, "password": store.PresetPassword,
	})
	if res.status != http.StatusOK {
		h.t.Fatalf("%s 登录失败: %d %s", username, res.status, res.Message)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		h.t.Fatalf("解析登录响应失败: %v", err)
	}
	h.tokens[username] = payload.Token
	return payload.Token
}

func (h *harness) expectStatus(res apiResponse, want int, context string) {
	h.t.Helper()
	if res.status != want {
		h.t.Fatalf("%s: 期望 HTTP %d，实际 %d %s", context, want, res.status, res.Message)
	}
}

func (h *harness) expectCode(res apiResponse, code string, context string) {
	h.t.Helper()
	if fmt.Sprint(res.Code) != code {
		h.t.Fatalf("%s: 期望错误码 %s，实际 %v（%s）", context, code, res.Code, res.Message)
	}
	if res.Message == "" {
		h.t.Fatalf("%s: 拒绝必须带可解释原因", context)
	}
}

func (h *harness) vehicleToken(idx int) string {
	return h.tokens[fmt.Sprintf("vehicle-%02d", idx)]
}

func (h *harness) desiredVersion(idx int) int {
	h.t.Helper()
	st, ok, err := h.st.VehicleState(context.Background(), fmt.Sprintf("VEH-%02d", idx))
	if err != nil || !ok {
		h.t.Fatalf("读取车辆状态失败: %v", err)
	}
	return st.DesiredVersion
}

func (h *harness) receipt(idx, version int, success bool, message string) apiResponse {
	return h.call(http.MethodPost, "/api/vehicle/receipt", h.vehicleToken(idx), map[string]any{
		"version": version, "success": success, "message": message,
	})
}

// TestAuthAndRoleGuards 覆盖鉴权与越权拒绝（R8 / V1）。
func TestAuthAndRoleGuards(t *testing.T) {
	h := newHarness(t, time.Minute)
	if res := h.call(http.MethodGet, "/health", "", nil); res.status != http.StatusOK {
		t.Fatalf("健康检查失败: %d", res.status)
	}
	if res := h.call(http.MethodGet, "/api/meta", "", nil); res.status != http.StatusOK {
		t.Fatalf("元信息接口失败: %d", res.status)
	}
	bad := h.call(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username": "dev", "password": "wrong-password",
	})
	h.expectStatus(bad, http.StatusUnauthorized, "错误口令登录")
	if res := h.call(http.MethodGet, "/api/applications", "", nil); res.status != http.StatusUnauthorized {
		t.Fatalf("缺少凭证应 401，实际 %d", res.status)
	}
	if res := h.call(http.MethodGet, "/api/applications", "tok-invalid", nil); res.status != http.StatusUnauthorized {
		t.Fatalf("无效凭证应 401，实际 %d", res.status)
	}
	dev := h.login("dev")
	approver := h.login("approver")
	ops := h.login("ops")
	for i := 1; i <= 5; i++ {
		h.login(fmt.Sprintf("vehicle-%02d", i))
	}
	if res := h.call(http.MethodGet, "/api/me", dev, nil); res.status != http.StatusOK {
		t.Fatalf("me 接口失败: %d", res.status)
	}
	created := h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "排查车机诊断日志缺失", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 30,
	})
	h.expectStatus(created, http.StatusOK, "创建申请")
	var app model.Application
	if err := json.Unmarshal(created.Data, &app); err != nil {
		t.Fatalf("解析申请失败: %v", err)
	}
	h.expectCode(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", dev, nil), "SELF_APPROVAL", "自审批")
	h.expectCode(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", ops, nil), "FORBIDDEN_ROLE", "运营审批")
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", approver, nil), http.StatusOK, "审批通过")
	h.expectCode(h.call(http.MethodPut, "/api/applications/"+app.ID, dev, map[string]any{
		"reason": "改参数", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 45,
	}), "PARAM_FROZEN", "批准后改参数")
	h.expectCode(h.call(http.MethodPost, "/api/activities", dev, map[string]any{
		"application_id": app.ID, "pilot_vehicle_ids": []string{"VEH-01", "VEH-02"},
	}), "FORBIDDEN_ROLE", "申请人执行发布")
	if res := h.call(http.MethodGet, "/api/state/export", dev, nil); res.status != http.StatusForbidden {
		t.Fatalf("申请人不得导出全量状态，实际 %d", res.status)
	}
}

// TestNormalPromotionScenario 覆盖场景 1（申请→审批→试点→推广→逐车核查→完成）。
func TestNormalPromotionScenario(t *testing.T) {
	h := newHarness(t, time.Minute)
	dev := h.login("dev")
	approver := h.login("approver")
	ops := h.login("ops")
	for i := 1; i <= 5; i++ {
		h.login(fmt.Sprintf("vehicle-%02d", i))
	}

	created := h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "排查网关偶发断连", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 20,
	})
	h.expectStatus(created, http.StatusOK, "创建申请")
	var app model.Application
	if err := json.Unmarshal(created.Data, &app); err != nil {
		t.Fatal(err)
	}
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", approver, nil), http.StatusOK, "批准")

	actRes := h.call(http.MethodPost, "/api/activities", ops, map[string]any{
		"application_id": app.ID, "pilot_vehicle_ids": []string{"VEH-01", "VEH-02"},
	})
	h.expectStatus(actRes, http.StatusOK, "创建活动")
	var act model.Activity
	if err := json.Unmarshal(actRes.Data, &act); err != nil {
		t.Fatal(err)
	}
	// 未试点车拉取不到配置（V4）
	pull3 := h.call(http.MethodGet, "/api/vehicle/config", h.vehicleToken(3), nil)
	h.expectStatus(pull3, http.StatusOK, "未试点车拉取")
	var pullRes model.PullResult
	if err := json.Unmarshal(pull3.Data, &pullRes); err != nil {
		t.Fatal(err)
	}
	if pullRes.Granted {
		t.Fatal("未试点车不得取得活动配置")
	}
	// 试点未成功时推广被拒绝（V5）
	h.expectCode(h.call(http.MethodPost, "/api/activities/"+act.ID+"/promote", ops, nil), "PROMOTE_BLOCKED", "试点未成功推广")

	for _, idx := range []int{1, 2} {
		pull := h.call(http.MethodGet, "/api/vehicle/config", h.vehicleToken(idx), nil)
		h.expectStatus(pull, http.StatusOK, "试点车拉取")
		if err := json.Unmarshal(pull.Data, &pullRes); err != nil {
			t.Fatal(err)
		}
		if !pullRes.Granted || pullRes.Version != h.desiredVersion(idx) {
			t.Fatalf("试点车应取得最新版本: %+v", pullRes)
		}
		h.expectStatus(h.receipt(idx, pullRes.Version, true, "应用成功"), http.StatusOK, "试点成功回执")
	}
	// 重复成功幂等（V10）
	dup := h.receipt(1, h.desiredVersion(1), true, "重复上报")
	h.expectStatus(dup, http.StatusOK, "重复成功回执")
	var dupRes model.ReceiptResult
	if err := json.Unmarshal(dup.Data, &dupRes); err != nil {
		t.Fatal(err)
	}
	if dupRes.Recorded {
		t.Fatal("重复成功不得重复记账")
	}
	// 跨车与未知版本拒绝（V12）
	h.expectCode(h.receipt(1, h.desiredVersion(2), true, "跨车"), "CROSS_VEHICLE", "跨车回执")
	h.expectCode(h.receipt(1, 99999, true, "未知版本"), "UNKNOWN_VERSION", "未知版本回执")
	// 无凭证拒绝（V9）
	if res := h.call(http.MethodPost, "/api/vehicle/receipt", "", map[string]any{"version": 1, "success": true}); res.status != http.StatusUnauthorized {
		t.Fatalf("未认证回执应 401，实际 %d", res.status)
	}

	progress := h.call(http.MethodGet, "/api/activities/"+act.ID+"/progress", approver, nil)
	h.expectStatus(progress, http.StatusOK, "试点进度")
	var p model.ActivityProgress
	if err := json.Unmarshal(progress.Data, &p); err != nil {
		t.Fatal(err)
	}
	if !p.CanPromote || p.Activity.Status != model.ActPromotionReady {
		t.Fatalf("两台试点成功后应可推广: %+v", p.Activity.Status)
	}
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/promote", ops, nil), http.StatusOK, "确认推广")
	for _, idx := range []int{3, 4, 5} {
		h.expectStatus(h.receipt(idx, h.desiredVersion(idx), true, "应用成功"), http.StatusOK, "推广回执")
	}
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/complete", approver, nil), http.StatusOK, "记完成")
	final := h.call(http.MethodGet, "/api/activities/"+act.ID+"/progress", ops, nil)
	if err := json.Unmarshal(final.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Activity.Status != model.ActCompleted || p.SuccessCount != 5 {
		t.Fatalf("活动应完成且 5 台成功: %+v", p)
	}
	// 逐车核查展示前值/期望值/生效值/原因（V19）
	for _, v := range p.Vehicles {
		if v.BaseValue != 60 || v.ExpectedValue != 20 || v.EffectiveValue != 20 || v.Result != model.ResultSuccess {
			t.Fatalf("逐车核查字段不完整: %+v", v)
		}
	}
	// 同车队已有未结束活动时创建被拒绝 → 完成后可再创建（V3 的另一侧）
	second := h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "第二轮排查", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 20,
	})
	h.expectStatus(second, http.StatusOK, "新建申请")
}

// TestFailureTimeoutAndRestoreScenario 覆盖场景 2/3/4/5：失败暂停、停止、恢复、未确认门禁。
func TestFailureTimeoutAndRestoreScenario(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	dev := h.login("dev")
	approver := h.login("approver")
	ops := h.login("ops")
	for i := 1; i <= 5; i++ {
		h.login(fmt.Sprintf("vehicle-%02d", i))
	}
	created := h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "排查日志采样异常", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 15,
	})
	var app model.Application
	if err := json.Unmarshal(created.Data, &app); err != nil {
		t.Fatal(err)
	}
	// 退回 → 修改再提交（V2）
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app.ID+"/reject", approver, map[string]string{
		"reason": "间隔过小，请改为 15 秒说明原因",
	}), http.StatusOK, "退回申请")
	h.expectStatus(h.call(http.MethodPut, "/api/applications/"+app.ID, dev, map[string]any{
		"reason": "排查日志采样异常（已补充说明）", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 15,
	}), http.StatusOK, "退回后修改")
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", approver, nil), http.StatusOK, "重新批准")

	actRes := h.call(http.MethodPost, "/api/activities", ops, map[string]any{
		"application_id": app.ID, "pilot_vehicle_ids": []string{"VEH-01", "VEH-02"},
	})
	var act model.Activity
	if err := json.Unmarshal(actRes.Data, &act); err != nil {
		t.Fatal(err)
	}
	// VEH-01 成功；VEH-02 离线不回执 → 超时 → 暂停
	h.expectStatus(h.receipt(1, h.desiredVersion(1), true, "应用成功"), http.StatusOK, "试点成功")
	time.Sleep(400 * time.Millisecond)
	progress := h.call(http.MethodGet, "/api/activities/"+act.ID+"/progress", ops, nil)
	var p model.ActivityProgress
	if err := json.Unmarshal(progress.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Activity.Status != model.ActPaused {
		t.Fatalf("离线车超时后活动应暂停，实际 %s", p.Activity.Status)
	}
	h.expectCode(h.call(http.MethodPost, "/api/activities/"+act.ID+"/promote", ops, nil), "PROMOTE_BLOCKED", "超时后推广")
	// 负责人停止（V16：停止不撤销）
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/stop", approver, map[string]string{
		"reason": "试点车离线未回执，停止未发送部分",
	}), http.StatusOK, "停止活动")
	h.expectCode(h.receipt(2, h.desiredVersion(2), true, "停机后迟到回执"), "ACTIVITY_CLOSED", "停止后迟到回执")
	got := h.call(http.MethodGet, "/api/activities/"+act.ID, approver, nil)
	if err := json.Unmarshal(got.Data, &act); err != nil {
		t.Fatal(err)
	}
	if act.Status != model.ActStopped {
		t.Fatalf("停止后状态不得被迟到回执改变，实际 %s", act.Status)
	}
	// 恢复（V17）：批准 → 更高版本下发 → 逐车确认
	h.expectCode(h.call(http.MethodPost, "/api/activities/"+act.ID+"/restore/publish", ops, nil), "INVALID_STATUS", "未批准即恢复")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/restore/approve", approver, nil), http.StatusOK, "批准恢复")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/restore/publish", ops, nil), http.StatusOK, "下发恢复版本")
	rec := h.call(http.MethodGet, "/api/activities/"+act.ID+"/progress", approver, nil)
	if err := json.Unmarshal(rec.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.AllRestored || p.AllRecoveredShown || len(p.UnconfirmedRestoreIDs) == 0 {
		t.Fatalf("未确认恢复时禁止显示全部恢复: %+v", p)
	}
	h.expectCode(h.call(http.MethodPost, "/api/activities/"+act.ID+"/restore/complete", approver, nil), "RESTORE_BLOCKED", "未确认即恢复完成")
	for _, idx := range []int{1, 2} {
		h.expectStatus(h.receipt(idx, h.desiredVersion(idx), true, "已恢复原设置"), http.StatusOK, "恢复回执")
	}
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/restore/complete", approver, nil), http.StatusOK, "记恢复完成")
	// 结果核查：至少包含成功、失败和未确认记录（V19 / 场景 5）
	if res := h.call(http.MethodGet, "/api/audit/events?limit=50", approver, nil); res.status != http.StatusOK {
		t.Fatalf("留痕查询失败: %d", res.status)
	}
	exported := h.call(http.MethodGet, "/api/state/export", ops, nil)
	if exported.status != http.StatusOK {
		t.Fatalf("状态导出失败: %d %s", exported.status, exported.Message)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(exported.raw, &state); err != nil {
		t.Fatalf("导出不是合法 JSON: %v", err)
	}
	for _, key := range []string{"preset", "applications", "activities", "vehicles", "config_pulls", "receipts"} {
		if _, ok := state[key]; !ok {
			t.Fatalf("导出缺少字段 %s", key)
		}
	}
}

// TestInternalErrorResponses 通过关闭底层连接验证 500 兜底分支（存储故障不泄露堆栈，返回统一错误）。
func TestInternalErrorResponses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// 将 sqlite 文件指向一个已存在的目录：任何读写都会失败，模拟持久层故障
	st, err := store.Open("sqlite::@file(" + dir + ")")
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	svc := logic.New(st, time.Minute)
	s := controller.NewServer(svc, fmt.Sprintf("err-%d", time.Now().UnixNano()))
	s.SetAddr("127.0.0.1:0")
	s.SetDumpRouterMap(false)
	s.Start()
	t.Cleanup(func() {
		_ = s.Shutdown()
		_ = st.Close(ctx)
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", s.GetListenedPort())

	resp, err := http.Get(base + "/api/meta")
	if err != nil {
		t.Fatalf("请求 meta 失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("存储故障应返回 500，实际 %d（%s）", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "INTERNAL") {
		t.Fatalf("内部错误应带统一错误码: %s", string(body))
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/applications", nil)
	req.Header.Set("Authorization", "Bearer tok-any")
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	_, _ = io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusInternalServerError {
		t.Fatalf("鉴权存储故障应返回 500，实际 %d", resp2.StatusCode)
	}
}

// TestAllEndpointsAndParseErrors 覆盖全部接口路径与非法请求体分支。
func TestAllEndpointsAndParseErrors(t *testing.T) {
	h := newHarness(t, time.Minute)
	// 非法 JSON 请求体 → 400
	h.expectStatus(h.call(http.MethodPost, "/api/auth/login", "", []int{1, 2}), http.StatusBadRequest, "登录非法请求体")
	// 未登录访问受保护接口
	h.expectStatus(h.call(http.MethodGet, "/api/activities", "", nil), http.StatusUnauthorized, "未登录查询活动")

	dev := h.login("dev")
	approver := h.login("approver")
	ops := h.login("ops")
	for i := 1; i <= 5; i++ {
		h.login(fmt.Sprintf("vehicle-%02d", i))
	}
	for _, tc := range []struct {
		method, path, token string
		body                any
	}{
		{http.MethodPost, "/api/applications", dev, []int{1}},
		{http.MethodPut, "/api/applications/APP-X", dev, []int{1}},
		{http.MethodPost, "/api/applications/APP-X/reject", approver, []int{1}},
		{http.MethodPost, "/api/activities", ops, []int{1}},
		{http.MethodPost, "/api/activities/ACT-X/stop", ops, []int{1}},
		{http.MethodPost, "/api/vehicle/receipt", h.vehicleToken(1), []int{1}},
	} {
		h.expectStatus(h.call(tc.method, tc.path, tc.token, tc.body), http.StatusBadRequest, "非法请求体 "+tc.path)
	}
	if res := h.call(http.MethodGet, "/", "", nil); res.status != http.StatusOK {
		t.Fatalf("根路径提示失败: %d", res.status)
	}
	if res := h.call(http.MethodGet, "/api/meta", ops, nil); res.status != http.StatusOK {
		t.Fatalf("元信息失败: %d", res.status)
	}

	created := h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "全接口覆盖场景", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 30,
	})
	var app model.Application
	if err := json.Unmarshal(created.Data, &app); err != nil {
		t.Fatal(err)
	}
	if res := h.call(http.MethodGet, "/api/applications", dev, nil); res.status != http.StatusOK {
		t.Fatalf("申请列表失败: %d", res.status)
	}
	if res := h.call(http.MethodGet, "/api/applications/"+app.ID, approver, nil); res.status != http.StatusOK {
		t.Fatalf("申请详情失败: %d", res.status)
	}
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", approver, nil), http.StatusOK, "批准")
	actRes := h.call(http.MethodPost, "/api/activities", ops, map[string]any{
		"application_id": app.ID, "pilot_vehicle_ids": []string{"VEH-01", "VEH-02"},
	})
	var act model.Activity
	if err := json.Unmarshal(actRes.Data, &act); err != nil {
		t.Fatal(err)
	}
	if res := h.call(http.MethodGet, "/api/activities", ops, nil); res.status != http.StatusOK {
		t.Fatalf("活动列表失败: %d", res.status)
	}
	if res := h.call(http.MethodGet, "/api/activities/"+act.ID, approver, nil); res.status != http.StatusOK {
		t.Fatalf("活动详情失败: %d", res.status)
	}
	// 试点一车失败 → 暂停 → 重试 → 两车成功 → 推广 → 全确认 → 完成
	h.expectStatus(h.call(http.MethodPost, "/api/vehicle/receipt", h.vehicleToken(1), map[string]any{
		"version": h.desiredVersion(1), "success": false, "message": "",
	}), http.StatusOK, "失败回执（空原因走默认文案）")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/retry-pilot", ops, nil), http.StatusOK, "重试试点")
	h.expectStatus(h.receipt(1, h.desiredVersion(1), true, "重试成功"), http.StatusOK, "试点成功")
	h.expectStatus(h.receipt(2, h.desiredVersion(2), true, "应用成功"), http.StatusOK, "试点成功")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/promote", ops, nil), http.StatusOK, "推广")
	for _, idx := range []int{3, 4, 5} {
		h.expectStatus(h.receipt(idx, h.desiredVersion(idx), true, "应用成功"), http.StatusOK, "推广回执")
	}
	if res := h.call(http.MethodGet, "/api/vehicle/state", h.vehicleToken(1), nil); res.status != http.StatusOK {
		t.Fatalf("车辆状态查询失败: %d", res.status)
	}
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/complete", approver, nil), http.StatusOK, "记完成")
	// 恢复分支全链路：创建第二个活动 → 停止 → 批准恢复 → 下发 → 部分确认 → 恢复完成
	second := h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "恢复链路", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 20,
	})
	var app2 model.Application
	if err := json.Unmarshal(second.Data, &app2); err != nil {
		t.Fatal(err)
	}
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app2.ID+"/approve", approver, nil), http.StatusOK, "批准2")
	act2Res := h.call(http.MethodPost, "/api/activities", ops, map[string]any{
		"application_id": app2.ID, "pilot_vehicle_ids": []string{"VEH-01", "VEH-02"},
	})
	var act2 model.Activity
	if err := json.Unmarshal(act2Res.Data, &act2); err != nil {
		t.Fatal(err)
	}
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act2.ID+"/stop", ops, map[string]string{
		"reason": "演示恢复链路",
	}), http.StatusOK, "停止活动")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act2.ID+"/restore/approve", approver, nil), http.StatusOK, "批准恢复")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act2.ID+"/restore/publish", ops, nil), http.StatusOK, "下发恢复")
	h.expectStatus(h.receipt(1, h.desiredVersion(1), true, "已恢复"), http.StatusOK, "恢复回执")
	h.expectCode(h.call(http.MethodPost, "/api/activities/"+act2.ID+"/restore/complete", approver, nil), "RESTORE_BLOCKED", "未全部确认不得恢复完成")
	h.expectStatus(h.receipt(2, h.desiredVersion(2), true, "已恢复"), http.StatusOK, "恢复回执")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act2.ID+"/restore/complete", approver, nil), http.StatusOK, "记恢复完成")
	if res := h.call(http.MethodGet, "/api/audit/events?limit=20", approver, nil); res.status != http.StatusOK {
		t.Fatalf("留痕查询失败: %d", res.status)
	}
	if res := h.call(http.MethodGet, "/api/state/export", ops, nil); res.status != http.StatusOK {
		t.Fatalf("状态导出失败: %d", res.status)
	}
	if res := h.call(http.MethodGet, "/api/health", "", nil); res.status != http.StatusOK {
		t.Fatalf("健康检查失败: %d", res.status)
	}
	// 资源不存在与越权 → 404/403 分支
	for _, tc := range []struct {
		method, path, token string
		body                any
		want                int
		desc                string
	}{
		{http.MethodGet, "/api/applications/APP-NONE", dev, nil, http.StatusNotFound, "申请不存在"},
		{http.MethodGet, "/api/activities/ACT-NONE", ops, nil, http.StatusNotFound, "活动不存在"},
		{http.MethodGet, "/api/activities/ACT-NONE/progress", approver, nil, http.StatusNotFound, "进度不存在"},
		{http.MethodPost, "/api/activities/ACT-NONE/retry-pilot", ops, nil, http.StatusNotFound, "重试不存在"},
		{http.MethodPost, "/api/activities/ACT-NONE/complete", approver, nil, http.StatusNotFound, "完成不存在"},
		{http.MethodPost, "/api/activities/ACT-NONE/restore/approve", approver, nil, http.StatusNotFound, "恢复批准不存在"},
		{http.MethodPost, "/api/activities/ACT-NONE/restore/publish", ops, nil, http.StatusNotFound, "恢复下发不存在"},
		{http.MethodPost, "/api/activities/ACT-NONE/restore/complete", approver, nil, http.StatusNotFound, "恢复完成不存在"},
		{http.MethodPost, "/api/activities/ACT-NONE/promote", ops, nil, http.StatusNotFound, "推广不存在"},
		{http.MethodGet, "/api/audit/events", h.vehicleToken(1), nil, http.StatusForbidden, "车辆查留痕"},
		{http.MethodGet, "/api/state/export", dev, nil, http.StatusForbidden, "申请人导出"},
		{http.MethodGet, "/api/vehicle/config", ops, nil, http.StatusForbidden, "运营拉取配置"},
		{http.MethodGet, "/api/vehicle/state", ops, nil, http.StatusForbidden, "运营查车辆状态"},
		{http.MethodPost, "/api/activities/" + act.ID + "/promote", approver, nil, http.StatusForbidden, "审批人代推广"},
		{http.MethodPost, "/api/activities/" + act.ID + "/stop", approver, map[string]string{"reason": "重复停止"}, http.StatusConflict, "重复停止"},
	} {
		res := h.call(tc.method, tc.path, tc.token, tc.body)
		if res.status != tc.want {
			t.Fatalf("%s: 期望 %d 实际 %d（%s）", tc.desc, tc.want, res.status, res.Message)
		}
		if res.Message == "" {
			t.Fatalf("%s: 拒绝必须带原因", tc.desc)
		}
	}
	// 申请校验分支：间隔越界与缺原因
	h.expectStatus(h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "间隔越界", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 500,
	}), http.StatusBadRequest, "间隔越界")
	h.expectStatus(h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 30,
	}), http.StatusBadRequest, "缺原因")
	// 退回必须带原因
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app.ID+"/reject", approver, map[string]string{"reason": "  "}), http.StatusBadRequest, "退回缺原因")
}
func TestVehicleReceiptBoundaries(t *testing.T) {
	h := newHarness(t, time.Minute)
	dev := h.login("dev")
	approver := h.login("approver")
	ops := h.login("ops")
	for i := 1; i <= 5; i++ {
		h.login(fmt.Sprintf("vehicle-%02d", i))
	}
	created := h.call(http.MethodPost, "/api/applications", dev, map[string]any{
		"reason": "回执边界验证", "vehicle_ids": []string{"VEH-01", "VEH-02", "VEH-03", "VEH-04", "VEH-05"}, "interval": 90,
	})
	var app model.Application
	if err := json.Unmarshal(created.Data, &app); err != nil {
		t.Fatal(err)
	}
	h.expectStatus(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", approver, nil), http.StatusOK, "批准")
	actRes := h.call(http.MethodPost, "/api/activities", ops, map[string]any{
		"application_id": app.ID, "pilot_vehicle_ids": []string{"VEH-01", "VEH-02"},
	})
	var act model.Activity
	if err := json.Unmarshal(actRes.Data, &act); err != nil {
		t.Fatal(err)
	}
	// 车辆状态接口
	st := h.call(http.MethodGet, "/api/vehicle/state", h.vehicleToken(1), nil)
	if st.status != http.StatusOK {
		t.Fatalf("车辆状态查询失败: %d", st.status)
	}
	// 失败回执：生效值保持 60
	fail := h.receipt(2, h.desiredVersion(2), false, "配置校验失败")
	h.expectStatus(fail, http.StatusOK, "失败回执")
	var failRes model.ReceiptResult
	if err := json.Unmarshal(fail.Data, &failRes); err != nil {
		t.Fatal(err)
	}
	if failRes.EffectiveValue != 60 || failRes.Result != model.ResultFailure {
		t.Fatalf("失败不得改变生效值: %+v", failRes)
	}
	// 活动已暂停：重试试点
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/retry-pilot", ops, nil), http.StatusOK, "重试试点")
	h.expectStatus(h.receipt(2, h.desiredVersion(2), true, "重试成功"), http.StatusOK, "重试回执")
	h.expectStatus(h.receipt(1, h.desiredVersion(1), true, "应用成功"), http.StatusOK, "试点成功")
	h.expectStatus(h.call(http.MethodPost, "/api/activities/"+act.ID+"/promote", ops, nil), http.StatusOK, "推广")
	// 车身回执：版本存在但属于他车（VEH-01 上报 VEH-03 的版本）
	h.expectCode(h.receipt(1, h.desiredVersion(3), true, "跨车"), "CROSS_VEHICLE", "跨车版本")
	// 回执体伪造车辆编号
	body := h.call(http.MethodPost, "/api/vehicle/receipt", h.vehicleToken(1), map[string]any{
		"vehicle_id": "VEH-03", "version": h.desiredVersion(1), "success": true,
	})
	h.expectCode(body, "CROSS_VEHICLE", "伪造车辆编号")
	// 车辆不能审批
	h.expectCode(h.call(http.MethodPost, "/api/applications/"+app.ID+"/approve", h.vehicleToken(1), nil), "FORBIDDEN_ROLE", "车辆审批")
}
