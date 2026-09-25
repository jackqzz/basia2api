package bps

import (
	"encoding/json"
	"testing"

	"bps-2api/internal/adapter"
)

// 窗口制套餐（prolite 等）：primary/secondary 窗口百分比映射。
func TestMapWhamUsageWindowedPlan(t *testing.T) {
	raw := `{
		"user_id": "user-x", "account_id": "acc", "email": "a@b.c",
		"plan_type": "self_serve_business_prolite",
		"rate_limit": {"allowed": true, "limit_reached": false,
			"primary_window": {"used_percent": 37, "limit_window_seconds": 18000, "reset_after_seconds": 3600},
			"secondary_window": {"used_percent": 12, "limit_window_seconds": 604800, "reset_after_seconds": 86400}},
		"credits": {"has_credits": false, "unlimited": false}
	}`
	var w whamPayload
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	var snap adapter.UsageSnapshot
	mapWhamUsage(&w, &snap)
	if snap.TotalPercentUsed == nil || *snap.TotalPercentUsed != 37 {
		t.Fatalf("total=%v, want 37", snap.TotalPercentUsed)
	}
	if snap.AutoPercentUsed == nil || *snap.AutoPercentUsed != 37 {
		t.Fatalf("auto=%v, want 37", snap.AutoPercentUsed)
	}
	if snap.APIPercentUsed == nil || *snap.APIPercentUsed != 12 {
		t.Fatalf("api=%v, want 12", snap.APIPercentUsed)
	}
	if snap.PlanLabel != "self_serve_business_prolite" {
		t.Fatalf("plan=%q", snap.PlanLabel)
	}
	if snap.QuotaDisableReason() != "" {
		t.Fatalf("37%% 不应熔断: %q", snap.QuotaDisableReason())
	}
}

// usage_based 欠费账号（workspace 余额耗尽）：rate_limit=null，必须顶满让
// QuotaDisableReason 熔断，否则页面显示"健康"但实际打不了上游。
func TestMapWhamUsageUsageBasedDepleted(t *testing.T) {
	raw := `{
		"user_id": "user-P8J5gjczTuuQhZyKLZtMU3cZ",
		"account_id": "0b1fd0c6-08e8-4177-b68a-0e401c65303f",
		"email": "pemnysaehl@gmail.com",
		"plan_type": "self_serve_business_usage_based",
		"rate_limit": null,
		"code_review_rate_limit": null,
		"additional_rate_limits": null,
		"model_usage": {"gpt-6-astra": {"available": false, "credits_would_enable": true}},
		"credits": {"has_credits": false, "unlimited": false, "overage_limit_reached": false, "balance": null},
		"spend_control": {"reached": false, "individual_limit": null},
		"rate_limit_reached_type": {"type": "workspace_member_credits_depleted"}
	}`
	var w whamPayload
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	var snap adapter.UsageSnapshot
	mapWhamUsage(&w, &snap)
	if snap.TotalPercentUsed == nil || *snap.TotalPercentUsed != quotaHardLimit {
		t.Fatalf("欠费账号 total=%v, want %v", snap.TotalPercentUsed, quotaHardLimit)
	}
	if snap.QuotaDisableReason() == "" {
		t.Fatal("欠费账号必须有熔断原因")
	}
	if snap.OnDemandLimitType != "workspace_member_credits_depleted" {
		t.Fatalf("limit_type=%q", snap.OnDemandLimitType)
	}
	if snap.AutoPercentUsed != nil || snap.APIPercentUsed != nil {
		t.Fatal("无窗口账号不应有窗口百分比")
	}
}

// usage_based 有余额：不熔断，消费上限/余额映射成 used/limit 美分。
func TestMapWhamUsageUsageBasedWithCredits(t *testing.T) {
	lim, bal := 5000.0, 1250.0
	var w whamPayload
	w.Credits.HasCredits = true
	w.Credits.Balance = &bal
	w.SpendControl.IndividualLimit = &lim
	var snap adapter.UsageSnapshot
	mapWhamUsage(&w, &snap)
	if snap.TotalPercentUsed != nil {
		t.Fatalf("有余额账号不应顶满: %v", *snap.TotalPercentUsed)
	}
	if snap.PlanLimitCents == nil || *snap.PlanLimitCents != 5000 {
		t.Fatalf("limit=%v", snap.PlanLimitCents)
	}
	if snap.PlanUsedCents == nil || *snap.PlanUsedCents != 3750 {
		t.Fatalf("used=%v, want 3750", snap.PlanUsedCents)
	}
	if snap.OnDemandEnabled == nil || !*snap.OnDemandEnabled {
		t.Fatal("has_credits 应开按需")
	}
	if snap.QuotaDisableReason() != "" {
		t.Fatalf("健康账号不应熔断: %q", snap.QuotaDisableReason())
	}
}

// 窗口打满（limit_reached=true）但百分比没回传 100：顶到硬限熔断。
func TestMapWhamUsageWindowLimitReached(t *testing.T) {
	raw := `{"rate_limit": {"allowed": false, "limit_reached": true,
		"primary_window": {"used_percent": 80, "limit_window_seconds": 18000}}}`
	var w whamPayload
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	var snap adapter.UsageSnapshot
	mapWhamUsage(&w, &snap)
	if snap.TotalPercentUsed == nil || *snap.TotalPercentUsed != quotaHardLimit {
		t.Fatalf("limit_reached total=%v", snap.TotalPercentUsed)
	}
	if snap.QuotaDisableReason() == "" {
		t.Fatal("打满窗口必须熔断")
	}
}
