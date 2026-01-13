package provider

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TencentCloudConfig 腾讯云市场通用配置
type TencentCloudConfig struct {
	SecretID  string
	SecretKey string
	BaseURL   string
	Method    string // GET, POST, etc.
	Timeout   time.Duration
}

// TencentCloudBase 腾讯云市场基础客户端
type TencentCloudBase struct {
	config *TencentCloudConfig
	client *http.Client
}

// NewTencentCloudBase 创建腾讯云基础客户端
func NewTencentCloudBase(config *TencentCloudConfig) *TencentCloudBase {
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}
	
	return &TencentCloudBase{
		config: config,
		client: &http.Client{
			Timeout: config.Timeout,
		},
	}
}

// DoRequest 执行腾讯云市场请求
func (b *TencentCloudBase) DoRequest(ctx context.Context, queryParams, bodyParams map[string]string) ([]byte, error) {

	// 检查配置是否为空
	if b.config.SecretID == "" || b.config.SecretKey == "" {
		return nil, fmt.Errorf("凭证缺失: SecretId 或 SecretKey 为空")
	}
	
	// 1. 构建 URL
	reqURL := b.config.BaseURL
	if len(queryParams) > 0 {
		reqURL = fmt.Sprintf("%s?%s", reqURL, urlencode(queryParams))
	}

	// 2. 构建 Body
	var body io.Reader
	bodyMethods := map[string]bool{"POST": true, "PUT": true, "PATCH": true}
	headers := make(map[string]string)
	
	if bodyMethods[b.config.Method] && len(bodyParams) > 0 {
		body = strings.NewReader(urlencode(bodyParams))
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}

	// 3. 创建请求
	req, err := http.NewRequestWithContext(ctx, b.config.Method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 4. 添加鉴权头
	auth, err := b.calcAuthorization()
	if err != nil {
		return nil, fmt.Errorf("计算签名失败: %w", err)
	}
	
	reqID := generateRequestID()
	headers["Authorization"] = auth
	headers["request-id"] = reqID

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// 5. 发起请求
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求发送失败: %w", err)
	}
	defer resp.Body.Close()

	// 6. 读取响应
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	return bodyBytes, nil
}

// calcAuthorization 计算腾讯云市场鉴权签名
func (b *TencentCloudBase) calcAuthorization() (string, error) {
	timeLocation, err := time.LoadLocation("Etc/GMT")
	if err != nil {
		timeLocation = time.UTC
	}

	datetime := time.Now().In(timeLocation).Format("Mon, 02 Jan 2006 15:04:05 GMT")
	signStr := fmt.Sprintf("x-date: %s", datetime)

	// HMAC-SHA1 签名
	mac := hmac.New(sha1.New, []byte(b.config.SecretKey))
	_, err = mac.Write([]byte(signStr))
	if err != nil {
		return "", err
	}
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
    
	auth := fmt.Sprintf(`{"id":"%s", "x-date":"%s", "signature":"%s"}`,
		b.config.SecretID, datetime, sign)

	return auth, nil
}

func urlencode(params map[string]string) string {
	values := url.Values{}
	for k, v := range params {
		values.Add(k, v)
	}
	return values.Encode()
}

func generateRequestID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}