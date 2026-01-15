package monitor

import (
    "encoding/json"
    "net/http"
    "sync"
    "time"
)

// Monitor 监控服务状态
type Monitor struct {
    mu sync.RWMutex

    StartTime      time.Time `json:"start_time"`       // 服务启动时间
    TotalRequests  int64     `json:"total_requests"`   // 调用上游总次数
    SuccessCount   int64     `json:"success_count"`    // 成功次数
    FailCount      int64     `json:"fail_count"`       // 失败次数
    ConsecutiveErr int64     `json:"consecutive_err"`  // 连续失败次数
    LastError      string    `json:"last_error"`       // 最后一次错误信息
    LastErrorTime  time.Time `json:"last_error_time"`  // 最后一次出错时间
    LastFailIP     string    `json:"last_fail_ip"`     // 导致出错的 IP
    RemainingRequestNum int64 `json:"remaining_request_num"` // 剩余配额
    CacheItemCount int64     `json:"cache_item_count"`

    quotaFetcher func() int64
    cacheFetcher func() int64
}

func New() *Monitor {
    return &Monitor{
        StartTime:           time.Now(),
        RemainingRequestNum: -1,
        CacheItemCount:      0,
    }
}

func (m *Monitor) SetCacheFetcher(f func() int64) {
    m.cacheFetcher = f
}

func (m *Monitor) SetQuotaFetcher(f func() int64) {
    m.quotaFetcher = f
}

// RecordSuccess 记录一次成功
func (m *Monitor) RecordSuccess() {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.TotalRequests++
    m.SuccessCount++
    m.ConsecutiveErr = 0 // 重置连续失败计数
}

// RecordFailure 记录一次失败
func (m *Monitor) RecordFailure(ip string, errMsg string) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.TotalRequests++
    m.FailCount++
    m.ConsecutiveErr++
    
    m.LastError = errMsg
    m.LastFailIP = ip
    m.LastErrorTime = time.Now()
}

// HandleStatus HTTP 接口处理函数
func (m *Monitor) HandleStatus(w http.ResponseWriter, r *http.Request) {
    // 1. 更新配额 (Quota)
    if m.quotaFetcher != nil {
        newQuota := m.quotaFetcher()
        if newQuota >= 0 {
            m.mu.Lock()
            m.RemainingRequestNum = newQuota
            m.mu.Unlock()
        }
    }

    if m.cacheFetcher != nil {
        count := m.cacheFetcher()
        m.mu.Lock()
        m.CacheItemCount = count
        m.mu.Unlock()
    }

    type monitorSnapshot struct {
        StartTime      time.Time `json:"start_time"`
        TotalRequests  int64     `json:"total_requests"`
        SuccessCount   int64     `json:"success_count"`
        FailCount      int64     `json:"fail_count"`
        ConsecutiveErr int64     `json:"consecutive_err"`
        LastError      string    `json:"last_error"`
        LastErrorTime  time.Time `json:"last_error_time"`
        LastFailIP     string    `json:"last_fail_ip"`
        RemainingRequestNum int64 `json:"remaining_request_num"`
        CacheItemCount int64     `json:"cache_item_count"`
    }

    var snap monitorSnapshot

    m.mu.RLock()
    snap.StartTime = m.StartTime
    snap.TotalRequests = m.TotalRequests
    snap.SuccessCount = m.SuccessCount
    snap.FailCount = m.FailCount
    snap.ConsecutiveErr = m.ConsecutiveErr
    snap.LastError = m.LastError
    snap.LastErrorTime = m.LastErrorTime
    snap.LastFailIP = m.LastFailIP
    snap.RemainingRequestNum = m.RemainingRequestNum
    snap.CacheItemCount = m.CacheItemCount
    m.mu.RUnlock()

    status := struct {
        Healthy     bool             `json:"healthy"`
        Uptime      string           `json:"uptime"`
        MonitorData *monitorSnapshot `json:"data"`
    }{
        Healthy:     snap.ConsecutiveErr < 3,
        Uptime:      time.Since(snap.StartTime).String(),
        MonitorData: &snap,
    }

    w.Header().Set("Content-Type", "application/json")
    if !status.Healthy {
        w.WriteHeader(http.StatusInternalServerError)
    } else {
        w.WriteHeader(http.StatusOK)
    }

    json.NewEncoder(w).Encode(status)
}