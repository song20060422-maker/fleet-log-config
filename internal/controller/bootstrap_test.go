package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gogf/gf/v2/os/gctx"
)

func TestMaskLinkHidesPassword(t *testing.T) {
	cases := map[string]string{
		"mysql:root:root@tcp(127.0.0.1:3307)/fleet_log?charset=utf8mb4": "mysql:root:***@tcp(127.0.0.1:3307)/fleet_log?charset=utf8mb4",
		"sqlite::@file(./data/fleet.db)":                                "sqlite::@file(./data/fleet.db)",
		"plain-link-without-at":                                         "plain-link-without-at",
		"@leading-at":                                                   "@leading-at",
	}
	for in, want := range cases {
		if got := maskLink(in); got != want {
			t.Fatalf("maskLink(%q)=%q, 期望 %q", in, got, want)
		}
	}
}

func TestSQLiteDirParsesFileDir(t *testing.T) {
	if got := sqliteDir("sqlite::@file(./data/fleet.db)"); got != "data" {
		t.Fatalf("sqliteDir= %q, 期望 data", got)
	}
	if got := sqliteDir("sqlite::@file(./data/fleet.db?cache=shared)"); got != "data" {
		t.Fatalf("带参数解析失败: %q", got)
	}
	if got := sqliteDir("mysql:root:root@tcp(127.0.0.1:3307)/fleet_log"); got != "" {
		t.Fatalf("MySQL 链接不应解析出目录: %q", got)
	}
}

func TestOpenStoreUsesEnvLinkAndInitializes(t *testing.T) {
	dir := t.TempDir()
	link := "sqlite::@file(" + filepath.Join(dir, "bootstrap.db") + ")"
	t.Setenv("FLEET_DB_LINK", link)
	ctx := gctx.GetInitCtx()
	st, err := OpenStore(ctx)
	if err != nil {
		t.Fatalf("OpenStore 失败: %v", err)
	}
	defer func() { _ = st.Close(context.Background()) }()
	if st.Dialect() != "sqlite" {
		t.Fatalf("方言应为 sqlite, 实际 %s", st.Dialect())
	}
	vehicles, err := st.Vehicles(ctx)
	if err != nil || len(vehicles) != 5 {
		t.Fatalf("预置车辆应为 5 台: %v %v", len(vehicles), err)
	}
}

func TestOpenStoreRejectsBadLink(t *testing.T) {
	t.Setenv("FLEET_DB_LINK", "oracle:scott:tiger@127.0.0.1:1521/orcl")
	if _, err := OpenStore(gctx.GetInitCtx()); err == nil {
		t.Fatal("不支持的链接应返回错误")
	}
}
