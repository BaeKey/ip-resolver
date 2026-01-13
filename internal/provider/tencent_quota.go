package provider

import (
	"log"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	market "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/market/v20191010"
)

// TencentQuotaChecker
type TencentQuotaChecker struct {
	InstanceID string
	// ★★★ 修改 1: 保存 Client 实例，而不是保存 Key ★★★
	Client *market.Client
}

// NewTencentQuotaChecker 初始化时就创建好 Client
func NewTencentQuotaChecker(secretID, secretKey, instanceID string) *TencentQuotaChecker {
	// 1. 初始化凭证
	credential := common.NewCredential(secretID, secretKey)

	// 2. 初始化配置
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "market.tencentcloudapi.com"
	cpf.HttpProfile.ReqTimeout = 5

	// 3. 创建 Client
	// 注意：这里 Region 填空字符串 "" 即可，因为我们要指定 Endpoint
	client, err := market.NewClient(credential, "", cpf)
	if err != nil {
		// 如果初始化失败，打印日志，但不要 panic，防止程序崩掉
		log.Printf("[Quota] Init Client Error: %v", err)
		return &TencentQuotaChecker{
			InstanceID: instanceID,
			Client:     nil, // 标记为不可用
		}
	}

	return &TencentQuotaChecker{
		InstanceID: instanceID,
		Client:     client,
	}
}

// GetRemainingRequests 查询时直接使用现成的 Client
func (c *TencentQuotaChecker) GetRemainingRequests() int64 {
	// 如果初始化时失败了，或者没有 InstanceID，直接返回
	if c.Client == nil || c.InstanceID == "" {
		return -1
	}

	// 4. 构造请求
	request := market.NewGetUsagePlanUsageAmountRequest()
	request.InstanceId = common.StringPtr(c.InstanceID)

	// 5. 发起调用 (复用 Client)
	response, err := c.Client.GetUsagePlanUsageAmount(request)
	if err != nil {
		log.Printf("[Quota] Fetch Error: %v", err)
		return -1
	}

	// 6. 返回结果
	if response.Response != nil && response.Response.RemainingRequestNum != nil {
		return int64(*response.Response.RemainingRequestNum)
	}

	return -1
}