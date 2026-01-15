package worker

import (
	"context"
	"ip-resolver/internal/cache"
	"ip-resolver/internal/config"
	"ip-resolver/internal/provider"
	"log"
	"net"
	"net/http"
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

	return &Manager{
		provider:  p,
		queue:     make(chan string, QueueSize),
		cache:     cache.New(ttl, ratio),
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