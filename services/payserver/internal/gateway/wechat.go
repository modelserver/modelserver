package gateway

import (
	"context"
	"fmt"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

type WeChatGatewayConfig struct {
	AppID             string
	MchID             string
	MchAPIv3Key       string
	MchSerialNo       string
	MchPrivateKeyPath string
	NotifyURL         string
}

type WeChatGateway struct {
	cfg       WeChatGatewayConfig
	nativeSvc *native.NativeApiService
}

func NewWeChatGateway(ctx context.Context, cfg WeChatGatewayConfig) (*WeChatGateway, error) {
	privKey, err := utils.LoadPrivateKeyWithPath(cfg.MchPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load wechat private key: %w", err)
	}

	client, err := core.NewClient(ctx,
		option.WithWechatPayAutoAuthCipher(cfg.MchID, cfg.MchSerialNo, privKey, cfg.MchAPIv3Key),
	)
	if err != nil {
		return nil, fmt.Errorf("create wechat client: %w", err)
	}

	return &WeChatGateway{
		cfg:       cfg,
		nativeSvc: &native.NativeApiService{Client: client},
	}, nil
}

func (g *WeChatGateway) Channel() string { return "wechat" }

func (g *WeChatGateway) CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error) {
	resp, _, err := g.nativeSvc.Prepay(ctx, native.PrepayRequest{
		Appid:       core.String(g.cfg.AppID),
		Mchid:       core.String(g.cfg.MchID),
		Description: core.String(req.Description),
		OutTradeNo:  core.String(req.OutTradeNo),
		NotifyUrl:   core.String(g.cfg.NotifyURL),
		Amount: &native.Amount{
			Total:    core.Int64(req.Amount),
			Currency: core.String("CNY"),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("wechat prepay: %w", err)
	}

	if resp == nil || resp.CodeUrl == nil {
		return nil, fmt.Errorf("wechat prepay: missing code_url in response")
	}

	return &PaymentResult{
		TradeNo:    "",
		PaymentURL: *resp.CodeUrl,
	}, nil
}
