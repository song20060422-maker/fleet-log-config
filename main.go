package main

import (
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gctx"

	"fleet/internal/controller"
)

func main() {
	if err := controller.Run(); err != nil {
		g.Log().Fatal(gctx.GetInitCtx(), err)
	}
}
