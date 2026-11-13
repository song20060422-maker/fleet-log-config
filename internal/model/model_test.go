package model_test

import (
	"errors"
	"testing"

	"fleet/internal/model"
)

func TestBizErrorFormatting(t *testing.T) {
	err := model.ErrConflict("PARAM_FROZEN", "申请 %s 参数冻结", "APP-000001")
	if err.Error() != "申请 APP-000001 参数冻结" {
		t.Fatalf("错误文案不正确: %s", err.Error())
	}
	if err.Status != 409 || err.Code != "PARAM_FROZEN" {
		t.Fatalf("错误码/状态不正确: %+v", err)
	}
	var target *model.BizError
	if !errors.As(error(err), &target) {
		t.Fatal("BizError 应可被 errors.As 识别")
	}
	if model.ErrBadRequest("X", "x").Status != 400 || model.ErrUnauthorized("u").Status != 401 ||
		model.ErrForbidden("F", "f").Status != 403 || model.ErrNotFound("n").Status != 404 {
		t.Fatal("错误状态映射不正确")
	}
	if model.IntervalMin != 10 || model.IntervalMax != 300 || model.InitialInterval != 60 {
		t.Fatal("预置口径不正确")
	}
	if model.FleetID != "FLEET-01" || model.PilotVehicleCount != 2 {
		t.Fatal("车队/试点数量口径不正确")
	}
	openStatuses := []string{model.ActPiloting, model.ActPromotionReady, model.ActPromoting, model.ActPaused, model.ActRecovering}
	for _, s := range openStatuses {
		if !model.OpenStatuses[s] {
			t.Fatalf("%s 应属于未结束状态", s)
		}
	}
}
