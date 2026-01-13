package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"ip-resolver/internal/model"
	"ip-resolver/internal/monitor"
)

type TencentIPQueryProvider struct {
	base *TencentCloudBase
	mon  *monitor.Monitor
}

func New_30498_Provider (secretID, secretKey string, mon *monitor.Monitor) *TencentIPQueryProvider {
	config := &TencentCloudConfig{
		SecretID:  secretID,
		SecretKey: secretKey,
		BaseURL:   "https://ap-guangzhou.cloudmarket-apigw.com/service-hnhpr5tw/ip/query",
		Method:    "POST",
	}

	return &TencentIPQueryProvider{
		base: NewTencentCloudBase(config),
		mon:  mon,
	}
}

func (p *TencentIPQueryProvider) Name() string {
	return "https://market.cloud.tencent.com/products/30498"
}

func (p *TencentIPQueryProvider) Fetch(ctx context.Context, ip string) (*model.IPInfo, error) {
	bodyParams := map[string]string{"ip": ip}
	
	bodyBytes, err := p.base.DoRequest(ctx, nil, bodyParams)
	if err != nil {
		p.mon.RecordFailure(ip, fmt.Sprintf("请求失败: %v", err))
		return nil, err
	}

	var apiResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Region string `json:"region"` // 省份
			ISP    string `json:"isp"`    // 运营商
		} `json:"data"`
	}

	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		p.mon.RecordFailure(ip, fmt.Sprintf("JSON解析失败: %v", err))
		return nil, fmt.Errorf("JSON解析失败: %w", err)
	}

	if apiResp.Code != 200 {
		errMsg := fmt.Sprintf("API 错误 | 代码: %d | 信息: %s", apiResp.Code, apiResp.Msg)
		p.mon.RecordFailure(ip, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	p.mon.RecordSuccess()

	return &model.IPInfo{
		Province: apiResp.Data.Region,
		ISP:      apiResp.Data.ISP,
	}, nil
}