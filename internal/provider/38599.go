package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"ip-resolver/internal/model"
	"ip-resolver/internal/monitor"
)

type ShuMaiProvider struct {
	base *TencentCloudBase
	mon  *monitor.Monitor
}

func New38599Provider(secretID, secretKey string, mon *monitor.Monitor) *ShuMaiProvider {
	config := &TencentCloudConfig{
		SecretID:  secretID,
		SecretKey: secretKey,
		BaseURL:   "https://ap-guangzhou.cloudmarket-apigw.com/service-5ezbz0ek/v4/ip/district/query",
		Method:    "GET",
	}

	return &ShuMaiProvider{
		base: NewTencentCloudBase(config),
		mon:  mon,
	}
}

func (p *ShuMaiProvider) Name() string {
	return "https://market.cloud.tencent.com/products/38599"
}

func (p *ShuMaiProvider) Fetch(ctx context.Context, ip string) (*model.IPInfo, error) {
	// 构建请求参数
	queryParams := map[string]string{
		"ip": ip,
	}

	// 发起请求
	bodyBytes, err := p.base.DoRequest(ctx, queryParams, nil)
	if err != nil {
		p.mon.RecordFailure(ip, fmt.Sprintf("请求失败: %v", err))
		return nil, err
	}

	// 解析响应
	var apiResp struct {
		Code    int    `json:"code"`
		Message string `json:"msg"`
		Success bool   `json:"success"`
		Data    struct {
			Result struct {
				Prov string `json:"prov"`
				ISP  string `json:"isp"`
			} `json:"result"`
		} `json:"data"`
	}

	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		p.mon.RecordFailure(ip, fmt.Sprintf("JSON解析失败: %v | body: %s", err, string(bodyBytes)))
		return nil, fmt.Errorf("JSON解析失败: %w", err)
	}

	if apiResp.Code != 200 {
		errMsg := fmt.Sprintf("API 错误 | 代码: %d | 信息: %s", apiResp.Code, apiResp.Message)
		p.mon.RecordFailure(ip, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	p.mon.RecordSuccess()

	return &model.IPInfo{
		Province: apiResp.Data.Result.Prov,
		ISP:      apiResp.Data.Result.ISP,
	}, nil
}