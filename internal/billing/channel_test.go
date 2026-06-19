// internal/billing/channel_test.go
package billing

import (
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestChannelPricing(t *testing.T) {
	plan := &types.Plan{PriceCNYFen: 11999, PriceUSDCents: 2000}
	freePlan := &types.Plan{}
	cnyOnly := &types.Plan{PriceCNYFen: 11999}
	usdOnly := &types.Plan{PriceUSDCents: 2000}

	cases := []struct {
		name     string
		channel  string
		plan     *types.Plan
		wantCur  string
		wantUnit int64
		wantOK   bool
	}{
		{"wechat ok", "wechat", plan, "CNY", 11999, true},
		{"alipay ok", "alipay", plan, "CNY", 11999, true},
		{"stripe ok", "stripe", plan, "USD", 2000, true},
		{"free plan via wechat", "wechat", freePlan, "CNY", 0, false},
		{"free plan via stripe", "stripe", freePlan, "USD", 0, false},
		{"cny-only via stripe", "stripe", cnyOnly, "USD", 0, false},
		{"usd-only via alipay", "alipay", usdOnly, "CNY", 0, false},
		{"unknown channel", "paypal", plan, "", 0, false},
		{"empty channel", "", plan, "", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur, unit, ok := ChannelPricing(tc.channel, tc.plan)
			if cur != tc.wantCur || unit != tc.wantUnit || ok != tc.wantOK {
				t.Fatalf("ChannelPricing(%q, ...) = (%q, %d, %v); want (%q, %d, %v)",
					tc.channel, cur, unit, ok, tc.wantCur, tc.wantUnit, tc.wantOK)
			}
		})
	}
}
