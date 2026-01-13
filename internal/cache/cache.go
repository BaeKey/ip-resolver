package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

// 配置常量
const (
	shardCount = 256
	shardMask  = shardCount - 1
	
	// 单个分片容量限制 (防止 OOM)
	defaultShardCapacity = 2000 
)

type entry struct {
	value     string
	exp       int64
	refreshAt int64
}

type Cache struct {
	shards [shardCount]*shard
	
	ttl           int64
	refreshWindow int64
	shardCap      int   // 单个分片容量限制

	now  int64
	stop chan struct{}
}

// 分片结构体
type shard struct {
	mu    sync.RWMutex
	items map[string]entry
}

// New 创建分片式高性能 Cache
func New(ttl time.Duration, refreshRatio float64) *Cache {
	if refreshRatio < 0 || refreshRatio >= 1 {
		refreshRatio = 0
	}

	c := &Cache{
		ttl:           int64(ttl),
		refreshWindow: int64(float64(ttl) * refreshRatio),
		shardCap:      defaultShardCapacity,
		now:           time.Now().UnixNano(),
		stop:          make(chan struct{}),
	}

	// 初始化所有分片
	for i := 0; i < shardCount; i++ {
		c.shards[i] = &shard{
			items: make(map[string]entry),
		}
	}

	c.startClock()
	c.startCleanup()

	return c
}

// getShard 根据 Key 计算哈希，定位到具体分片
func (c *Cache) getShard(key string) *shard {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return c.shards[h&shardMask]
}

// Get 读取缓存
func (c *Cache) Get(key string) (string, bool, bool, time.Duration) {
	now := atomic.LoadInt64(&c.now)
	
	// 1. 定位分片
	s := c.getShard(key)

	// 2. 读取
	s.mu.RLock()
	e, ok := s.items[key]
	s.mu.RUnlock()

	if !ok || now >= e.exp {
		return "", false, false, 0
	}

	needsRefresh := c.refreshWindow > 0 && now >= e.refreshAt
	remaining := time.Duration(e.exp - now)

	return e.value, true, needsRefresh, remaining
}

// Set 写入缓存
func (c *Cache) Set(key, val string) {
	now := atomic.LoadInt64(&c.now)
	exp := now + c.ttl
	
	e := entry{
		value:     val,
		exp:       exp,
		refreshAt: exp - c.refreshWindow,
	}

	// 1. 定位分片
	s := c.getShard(key)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 2. 检查容量 (防 OOM 策略)
	if len(s.items) >= c.shardCap {
		// 如果 key 本身已存在，只是更新，不算超容
		if _, exists := s.items[key]; !exists {
			// 随机驱逐 (Go map 遍历是随机的)
			for k := range s.items {
				delete(s.items, k)
				break 
			}
		}
	}

	s.items[key] = e
}

// Delete 删除
func (c *Cache) Delete(key string) {
	s := c.getShard(key)
	s.mu.Lock()
	delete(s.items, key)
	s.mu.Unlock()
}

func (c *Cache) Close() {
	close(c.stop)
}

// ================= 内部机制 =================

func (c *Cache) startClock() {
	ticker := time.NewTicker(time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				atomic.StoreInt64(&c.now, time.Now().UnixNano())
			case <-c.stop:
				return
			}
		}
	}()
}

// startCleanup 分片轮询清理
// 优点：永远不会锁住整个 Cache，每次只锁 1/256
func (c *Cache) startCleanup() {
	const cleanupInterval = 1 * time.Minute

	ticker := time.NewTicker(cleanupInterval)

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				now := atomic.LoadInt64(&c.now)
				// 逐个分片清理
				for i := 0; i < shardCount; i++ {
					c.cleanupShard(c.shards[i], now)
					// 让出 CPU，避免瞬间高负载
					time.Sleep(time.Millisecond * 5) 
				}
			case <-c.stop:
				return
			}
		}
	}()
}

func (c *Cache) cleanupShard(s *shard, now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.items {
		if now >= e.exp {
			delete(s.items, k)
		}
	}
}