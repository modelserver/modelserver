package billing

import "github.com/modelserver/modelserver/internal/types"

// ChannelPricing returns the currency and per-period unit price the given
// channel must charge for this plan. `ok=false` means the channel either is
// unsupported or has no price configured for this plan in its currency —
// callers should reject the order in either case.
//
// Adding a new channel: extend the switch with its currency + the plan
// column it reads from.
func ChannelPricing(channel string, plan *types.Plan) (currency string, unitPrice int64, ok bool) {
	switch channel {
	case "wechat", "alipay":
		return "CNY", plan.PriceCNYFen, plan.PriceCNYFen > 0
	case "stripe":
		return "USD", plan.PriceUSDCents, plan.PriceUSDCents > 0
	default:
		return "", 0, false
	}
}
