package worker

import (
	"context"
	"fmt"
	"ip-resolver/internal/cache"
	"ip-resolver/internal/config"
	"ip-resolver/internal/provider"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

)

/*
inflightSet：
- 核心去重组件
- 保证同一个 cacheKey(/24) 在“等待队列”或“执行中”只能存在一份
*/
type inflightSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func newInflightSet() *inflightSet {
	return &inflightSet{
		m: make(map[string]struct{}),
	}
}

func (s *inflightSet) TryAdd(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[key]; exists {
		return false
	}
	s.m[key] = struct{}{}
	return true
}

func (s *inflightSet) Delete(key string) {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
}

// ================= Manager ===================

type Manager struct {
	provider provider.IPProvider
	queue    chan string
	cache    *cache.Cache
	inflight *inflightSet
	wg       sync.WaitGroup
	debugMode bool
	cacheTTL  time.Duration
	concurrency int
}

// ======== 硬编码参数 =========
const (
	ApiRequestTimeout = 3 * time.Second
	QueueSize         = 4096
)

// ================= 构造 ===================

func NewManager(p provider.IPProvider, cfg *config.Config) *Manager {
	ratio := float64(cfg.CacheRefreshRatio) / 100.0
	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second

	c := cache.New(ttl, ratio)

	// 如果配置了持久化路径，尝试加载并开启自动保存
	if cfg.CacheStorePath != "" {
		if err := c.LoadFromSQLite(cfg.CacheStorePath); err != nil {
			log.Printf("尝试从 SQLite 加载缓存失败 (可能是首次启动): %v", err)
		}
		// 开启 Write-Behind 持久化 (批处理参数已内置)
		c.StartPersistence(cfg.CacheStorePath)
	}

	return &Manager{
		provider:  p,
		queue:     make(chan string, QueueSize),
		cache:     c,
		inflight:  newInflightSet(),
		debugMode: cfg.LogLevel == "debug",
		cacheTTL:  ttl,
		concurrency: cfg.WorkerConcurrency,
	}
}

func (m *Manager) debugLog(format string, v ...interface{}) {
	if m.debugMode {
		log.Printf("[DEBUG] "+format, v...)
	}
}

// ================= 工具函数 ===================

func getCacheKey(ip string) string {
	dot := 0
	for i := 0; i < len(ip); i++ {
		if ip[i] == '.' {
			dot++
			if dot == 3 {
				return ip[:i]
			}
		}
	}
	return ip
}

// ================= 启停 ===================

func (m *Manager) Start() {
	for i := 0; i < m.concurrency; i++ {
		m.wg.Add(1)
		go m.worker(i)
	}
}

func (m *Manager) Stop() {
	close(m.queue)
	m.wg.Wait()
	m.cache.Close()
}

// ================= HTTP Handler ===================

func (m *Manager) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	rawIP := strings.TrimPrefix(r.URL.Path, "/")

	if rawIP == "" || rawIP == "favicon.ico" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	parsedIP := net.ParseIP(rawIP)
	if parsedIP == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid ip format"))
		return
	}
	if parsedIP.To4() == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("only ipv4 supported"))
		return
	}

	cacheKey := getCacheKey(rawIP)

	tag, found, needsRefresh, remaining := m.cache.Get(cacheKey)
	if found {
		m.debugLog("缓存命中 | IP=%s | Key=%s | 剩余有效期=%v", rawIP, cacheKey, remaining)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(tag))

		if needsRefresh {
			if m.inflight.TryAdd(cacheKey) {
				m.debugLog("缓存预刷新 | Key=%s | 剩余有效期=%v", cacheKey, remaining)
				select {
				case m.queue <- rawIP:
				default:
					m.inflight.Delete(cacheKey)
				}
			}
		}
		return
	}

	m.debugLog("缓存未命中 | IP=%s | Key=%s", rawIP, cacheKey)

	if !m.inflight.TryAdd(cacheKey) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	select {
	case m.queue <- rawIP:
		w.WriteHeader(http.StatusAccepted)
	default:
		m.inflight.Delete(cacheKey)
		w.WriteHeader(http.StatusTooManyRequests)
	}
}

// ================= Worker ===================

func (m *Manager) worker(id int) {
	defer m.wg.Done()

	for rawIP := range m.queue {
		func() {
			cacheKey := getCacheKey(rawIP)
			defer m.inflight.Delete(cacheKey)

			_, found, needsRefresh, _ := m.cache.Get(cacheKey)
			if found && !needsRefresh {
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), ApiRequestTimeout)
			defer cancel()

			start := time.Now()

			info, err := m.provider.Fetch(ctx, rawIP)
			if err != nil {
				log.Printf("[Worker %d] 获取 %s 失败: %v", id, rawIP, err)
				return
			}

			info.Standardize()
			tag := info.ToTag()

			m.cache.Set(cacheKey, tag)

			m.debugLog("[Worker %d] %s (subnet=%s) -> %s | 耗时=%v", id, rawIP, cacheKey, tag, time.Since(start))
		}()
	}
}

func (m *Manager) GetCacheCount() int64 {
	if m.cache == nil {
		return 0
	}
	return m.cache.Count()
}

func (m *Manager) HandleStatistics(w http.ResponseWriter, r *http.Request) {
    // 1. 获取数据并处理可能的错误
    items, err := m.cache.GetAllItems()
    if err != nil {
        log.Printf("获取统计数据失败: %v", err)
        http.Error(w, "Failed to retrieve statistics from database", http.StatusInternalServerError)
        return
    }

    // map[tag][]string
    stats := make(map[string][]string)
    for k, v := range items {
        stats[v] = append(stats[v], k)
    }

    // Sort tags
    var tags []string
    for t := range stats {
        tags = append(tags, t)
    }
    sort.Strings(tags)

    // 2. 获取丢弃计数 (用于监控磁盘写入压力)
    droppedCount := m.cache.DroppedCount()

    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    
    // 在 HTML 中增加了 Dropped Updates 的展示
    fmt.Fprintf(w, `<html>
<head>
    <title>IP Cache Statistics</title>
    <style>
        body { font-family: sans-serif; }
        table { border-collapse: collapse; width: 100%%; }
        th, td { border: 1px solid #ddd; padding: 8px; text-align: left; }
        th { background-color: #f2f2f2; }
        .metric { margin-bottom: 20px; font-weight: bold; }
        .warn { color: red; }
    </style>
</head>
<body>
    <h1>IP Cache Statistics</h1>
    <div class="metric">
        <p>Total Cached Items: %d</p>
        <p>Dropped Updates (Disk Pressure): <span class="%s">%d</span></p>
    </div>
    <table>
        <tr>
            <th>Tag</th>
            <th>IP Ranges (Count)</th>
        </tr>`, 
        len(items), 
        func() string { if droppedCount > 0 { return "warn" } else { return "" } }(), //如果有丢弃显示红色
        droppedCount,
    )

    for _, tag := range tags {
        keys := stats[tag]
        sort.Strings(keys)
        
        // 为了展示没那么长，只展示前 50 个 + 计数
        displayKeys := keys
        if len(keys) > 50 {
            displayKeys = keys[:50]
            displayKeys = append(displayKeys, fmt.Sprintf("... and %d others", len(keys)-50))
        }
        
        fmt.Fprintf(w, "<tr><td>%s</td><td>%s <br/>(Count: %d)</td></tr>", 
            tag, strings.Join(displayKeys, ", "), len(keys))
    }
    fmt.Fprintf(w, "</table></body></html>")
}