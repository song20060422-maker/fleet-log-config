// Package controller 提供 HTTP 接口层（GoFrame ghttp）、路由装配与服务启动。
package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gctx"
	"github.com/gogf/gf/v2/os/gfile"

	"fleet/internal/logic"
	"fleet/internal/model"
	"fleet/internal/store"
)

// Controller 车队日志配置业务接口。
type Controller struct {
	svc *logic.Service
}

// New 创建接口控制器。
func New(svc *logic.Service) *Controller { return &Controller{svc: svc} }

// Bind 注册 /api 路由。
func (c *Controller) Bind(group *ghttp.RouterGroup) {
	group.POST("/auth/login", c.Login)
	group.GET("/meta", c.Meta)
	group.GET("/health", c.Health)

	group.Group("/", func(secured *ghttp.RouterGroup) {
		secured.Middleware(c.Auth)
		secured.GET("/me", c.Me)

		secured.POST("/applications", c.CreateApplication)
		secured.GET("/applications", c.ListApplications)
		secured.GET("/applications/:id", c.GetApplication)
		secured.PUT("/applications/:id", c.ReviseApplication)
		secured.POST("/applications/:id/approve", c.ApproveApplication)
		secured.POST("/applications/:id/reject", c.RejectApplication)

		secured.POST("/activities", c.CreateActivity)
		secured.GET("/activities", c.ListActivities)
		secured.GET("/activities/:id", c.GetActivity)
		secured.GET("/activities/:id/progress", c.Progress)
		secured.POST("/activities/:id/promote", c.Promote)
		secured.POST("/activities/:id/stop", c.Stop)
		secured.POST("/activities/:id/retry-pilot", c.RetryPilot)
		secured.POST("/activities/:id/complete", c.Complete)
		secured.POST("/activities/:id/restore/approve", c.ApproveRestore)
		secured.POST("/activities/:id/restore/publish", c.PublishRestore)
		secured.POST("/activities/:id/restore/complete", c.CompleteRestore)

		secured.GET("/vehicle/config", c.PullConfig)
		secured.POST("/vehicle/receipt", c.ReportReceipt)
		secured.GET("/vehicle/state", c.VehicleState)

		secured.GET("/audit/events", c.AuditEvents)
		secured.GET("/state/export", c.ExportState)
	})
}

// NewServer 装配 HTTP 服务（测试与运行共用同一装配路径）。
func NewServer(svc *logic.Service, names ...string) *ghttp.Server {
	args := make([]any, 0, len(names))
	for _, n := range names {
		args = append(args, n)
	}
	s := g.Server(args...)
	c := New(svc)
	s.Group("/api", func(group *ghttp.RouterGroup) {
		group.Middleware(ghttp.MiddlewareCORS)
		c.Bind(group)
	})
	s.BindHandler("GET:/health", c.Health)
	s.BindHandler("GET:/", c.Root)
	return s
}

// Run 读取配置、初始化数据库并启动服务。
func Run() error {
	ctx := gctx.GetInitCtx()
	link := g.Cfg().MustGet(ctx, "database.default.link", "sqlite::@file(./data/fleet.db)").String()
	timeout := g.Cfg().MustGet(ctx, "fleet.timeoutSeconds", model.DefaultTimeoutSecond).Int()
	if dir := sqliteDir(link); dir != "" {
		if err := gfile.Mkdir(dir); err != nil {
			return err
		}
	}
	st, err := store.Open(link)
	if err != nil {
		return err
	}
	if err := st.Init(ctx); err != nil {
		return err
	}
	svc := logic.New(st, time.Duration(timeout)*time.Second)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := svc.RefreshTimeouts(context.Background()); err != nil {
				g.Log().Warning(ctx, "刷新发布超时失败:", err)
			}
		}
	}()
	s := NewServer(svc)
	s.Run()
	return nil
}

// sqliteDir 从 sqlite link 中解析数据库文件所在目录（用于自动建目录）。
func sqliteDir(link string) string {
	idx := strings.Index(link, "@file(")
	if idx < 0 {
		return ""
	}
	rest := link[idx+len("@file("):]
	if end := strings.Index(rest, ")"); end >= 0 {
		rest = rest[:end]
	}
	return gfile.Dir(rest)
}

// ---------------------------------------------------------------- 通用响应

func ok(r *ghttp.Request, data any) {
	r.Response.WriteJson(g.Map{"code": 0, "message": "ok", "data": data})
}

func fail(r *ghttp.Request, err error) {
	var bizErr *model.BizError
	if errors.As(err, &bizErr) {
		r.Response.WriteHeader(bizErr.Status)
		r.Response.WriteJson(g.Map{"code": bizErr.Code, "message": bizErr.Message})
		return
	}
	r.Response.WriteHeader(500)
	r.Response.WriteJson(g.Map{"code": "INTERNAL", "message": err.Error()})
}

func userOf(r *ghttp.Request) model.User {
	if v := r.GetCtxVar("user"); v != nil {
		if u, ok := v.Interface().(model.User); ok {
			return u
		}
	}
	return model.User{}
}

// ---------------------------------------------------------------- 鉴权

// Auth 凭证鉴权：身份来自 token，禁止采信请求参数里的裸用户/车辆编号（R8）。
func (c *Controller) Auth(r *ghttp.Request) {
	token := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Token"))
	}
	if token == "" {
		fail(r, model.ErrUnauthorized("缺少访问凭证（请先登录，携带 Authorization: Bearer <token>）"))
		return
	}
	u, found, err := c.svc.Store().UserByToken(r.Context(), token)
	if err != nil {
		fail(r, err)
		return
	}
	if !found {
		fail(r, model.ErrUnauthorized("访问凭证无效或已过期"))
		return
	}
	r.SetCtxVar("user", u)
	r.Middleware.Next()
}

// Login 预置账号登录，返回 token 与角色信息。
func (c *Controller) Login(r *ghttp.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := r.Parse(&in); err != nil {
		fail(r, model.ErrBadRequest("INVALID_BODY", "登录参数解析失败：%s", err.Error()))
		return
	}
	u, found, err := c.svc.Store().UserByCredentials(r.Context(), in.Username, store.HashPassword(in.Password))
	if err != nil {
		fail(r, err)
		return
	}
	if !found {
		fail(r, model.ErrUnauthorized("账号或密码错误"))
		return
	}
	ok(r, g.Map{"token": store.TokenFor(u.Username), "user": u})
}

// Me 返回当前登录身份。
func (c *Controller) Me(r *ghttp.Request) { ok(r, userOf(r)) }

// Meta 演示元信息（车队/车辆/账号/口径）。
func (c *Controller) Meta(r *ghttp.Request) {
	data, err := c.svc.Meta(r.Context())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, data)
}

// Health 健康检查（CI 与部署冒烟使用）。
func (c *Controller) Health(r *ghttp.Request) {
	ok(r, g.Map{"status": "UP", "service": "fleet-log-config"})
}

// Root 根路径提示。
func (c *Controller) Root(r *ghttp.Request) {
	r.Response.WriteJson(g.Map{"service": "fleet-log-config", "api": "/api", "health": "/health"})
}

// ---------------------------------------------------------------- 申请与审批

// CreateApplication 创建申请。
func (c *Controller) CreateApplication(r *ghttp.Request) {
	var in model.ApplicationInput
	if err := r.Parse(&in); err != nil {
		fail(r, model.ErrBadRequest("INVALID_BODY", "申请参数解析失败：%s", err.Error()))
		return
	}
	app, err := c.svc.CreateApplication(r.Context(), userOf(r), in)
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, app)
}

// ReviseApplication 退回后修改再提交。
func (c *Controller) ReviseApplication(r *ghttp.Request) {
	var in model.ApplicationInput
	if err := r.Parse(&in); err != nil {
		fail(r, model.ErrBadRequest("INVALID_BODY", "申请参数解析失败：%s", err.Error()))
		return
	}
	app, err := c.svc.ReviseApplication(r.Context(), userOf(r), r.Get("id").String(), in)
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, app)
}

// ApproveApplication 批准申请。
func (c *Controller) ApproveApplication(r *ghttp.Request) {
	app, err := c.svc.ApproveApplication(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, app)
}

// RejectApplication 退回申请。
func (c *Controller) RejectApplication(r *ghttp.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := r.Parse(&in); err != nil {
		fail(r, model.ErrBadRequest("INVALID_BODY", "参数解析失败：%s", err.Error()))
		return
	}
	app, err := c.svc.RejectApplication(r.Context(), userOf(r), r.Get("id").String(), in.Reason)
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, app)
}

// GetApplication 申请详情。
func (c *Controller) GetApplication(r *ghttp.Request) {
	app, err := c.svc.GetApplication(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, app)
}

// ListApplications 申请列表。
func (c *Controller) ListApplications(r *ghttp.Request) {
	list, err := c.svc.ListApplications(r.Context(), userOf(r))
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, list)
}

// ---------------------------------------------------------------- 发布活动

// CreateActivity 创建活动并下发试点。
func (c *Controller) CreateActivity(r *ghttp.Request) {
	var in logic.CreateActivityInput
	if err := r.Parse(&in); err != nil {
		fail(r, model.ErrBadRequest("INVALID_BODY", "参数解析失败：%s", err.Error()))
		return
	}
	act, err := c.svc.CreateActivity(r.Context(), userOf(r), in)
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// ListActivities 活动列表。
func (c *Controller) ListActivities(r *ghttp.Request) {
	list, err := c.svc.ListActivities(r.Context(), userOf(r))
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, list)
}

// GetActivity 活动详情。
func (c *Controller) GetActivity(r *ghttp.Request) {
	act, err := c.svc.GetActivity(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// Progress 逐车进度与门禁判定。
func (c *Controller) Progress(r *ghttp.Request) {
	p, err := c.svc.Progress(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, p)
}

// Promote 确认推广。
func (c *Controller) Promote(r *ghttp.Request) {
	act, err := c.svc.Promote(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// Stop 停止活动。
func (c *Controller) Stop(r *ghttp.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := r.Parse(&in); err != nil {
		fail(r, model.ErrBadRequest("INVALID_BODY", "参数解析失败：%s", err.Error()))
		return
	}
	act, err := c.svc.StopActivity(r.Context(), userOf(r), r.Get("id").String(), in.Reason)
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// RetryPilot 重试试点。
func (c *Controller) RetryPilot(r *ghttp.Request) {
	act, err := c.svc.RetryPilot(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// Complete 记为完成。
func (c *Controller) Complete(r *ghttp.Request) {
	act, err := c.svc.CompleteActivity(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// ApproveRestore 批准恢复。
func (c *Controller) ApproveRestore(r *ghttp.Request) {
	act, err := c.svc.ApproveRestore(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// PublishRestore 下发恢复版本。
func (c *Controller) PublishRestore(r *ghttp.Request) {
	act, err := c.svc.PublishRestore(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// CompleteRestore 记为恢复完成。
func (c *Controller) CompleteRestore(r *ghttp.Request) {
	act, err := c.svc.CompleteRestore(r.Context(), userOf(r), r.Get("id").String())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, act)
}

// ---------------------------------------------------------------- 模拟车辆

// PullConfig 车辆拉取配置。
func (c *Controller) PullConfig(r *ghttp.Request) {
	res, err := c.svc.PullConfig(r.Context(), userOf(r))
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, res)
}

// ReportReceipt 车辆上报回执。
func (c *Controller) ReportReceipt(r *ghttp.Request) {
	var in model.ReceiptInput
	if err := r.Parse(&in); err != nil {
		fail(r, model.ErrBadRequest("INVALID_BODY", "参数解析失败：%s", err.Error()))
		return
	}
	res, err := c.svc.ReportReceipt(r.Context(), userOf(r), in)
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, res)
}

// VehicleState 车辆查询自身期望值/生效值与本车拉取记录。
func (c *Controller) VehicleState(r *ghttp.Request) {
	st, err := c.svc.VehicleState(r.Context(), userOf(r))
	if err != nil {
		fail(r, err)
		return
	}
	pulls, err := c.svc.Store().ConfigPullsOfVehicle(r.Context(), st.VehicleID)
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, g.Map{"state": st, "pulls": pulls})
}

// ---------------------------------------------------------------- 核查与导出

// AuditEvents 留痕事件列表。
func (c *Controller) AuditEvents(r *ghttp.Request) {
	list, err := c.svc.AuditEvents(r.Context(), userOf(r), r.Get("limit").Int())
	if err != nil {
		fail(r, err)
		return
	}
	ok(r, list)
}

// ExportState 业务状态导出（Skill 核查输入）。
func (c *Controller) ExportState(r *ghttp.Request) {
	data, err := c.svc.ExportState(r.Context(), userOf(r))
	if err != nil {
		fail(r, err)
		return
	}
	r.Response.WriteJson(data)
}
