package cache

import (
    "context"
    "database/sql"
    "fmt"
    "log"
    "sync"
    "sync/atomic"
    "time"

    _ "modernc.org/sqlite"
)

// ================= 配置常量 =================

const (
    shardCount = 256
    shardMask  = shardCount - 1

    defaultShardCapacity = 2000

    persistBatchSize = 100
    persistInterval  = 2 * time.Second
    cleanupInterval  = 30 * time.Minute
)

// ================= 结构定义 =================

type persistenceOp struct {
    IsDelete  bool
    Key       string
    Value     string
    Exp       int64
    RefreshAt int64
}

type entry struct {
    value     string
    exp       int64
    refreshAt int64
}

type shard struct {
    mu    sync.RWMutex
    items map[string]entry
}

type Cache struct {
    shards [shardCount]*shard

    ttl           int64
    refreshWindow int64
    shardCap      int

    // 统计指标
    count          int64
    droppedUpdates int64

    now int64

    stop      chan struct{}
    persistCh chan persistenceOp

    // === 数据库并发控制 ===
    // 使用读写锁保护 dbPath 和 roDB，替代 sync.Once 以处理更复杂的初始化逻辑
    dbMu   sync.RWMutex
    dbPath string
    roDB   *sql.DB

    wg     sync.WaitGroup
    closed int32 // 0 = open, 1 = closed
}

// ================= 构造函数 =================

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
        persistCh:     make(chan persistenceOp, 2048),
    }

    for i := 0; i < shardCount; i++ {
        c.shards[i] = &shard{
            items: make(map[string]entry),
        }
    }

    c.startClock()
    c.startCleanup()

    return c
}

// ================= Shard & Hash =================

func (c *Cache) getShard(key string) *shard {
    var h uint64 = 14695981039346656037
    for i := 0; i < len(key); i++ {
        h ^= uint64(key[i])
        h *= 1099511628211
    }
    return c.shards[h&shardMask]
}

// ================= 核心读写逻辑 =================

func (c *Cache) Get(key string) (string, bool, bool, time.Duration) {
    now := atomic.LoadInt64(&c.now)
    s := c.getShard(key)

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

func (c *Cache) Set(key, val string) {
    now := atomic.LoadInt64(&c.now)
    exp := now + c.ttl

    e := entry{
        value:     val,
        exp:       exp,
        refreshAt: exp - c.refreshWindow,
    }

    s := c.getShard(key)
    s.mu.Lock()

    if _, exists := s.items[key]; exists {
        s.items[key] = e
        s.mu.Unlock()
        c.sendToPersist(persistenceOp{
            Key: key, Value: val, Exp: exp, RefreshAt: e.refreshAt,
        })
        return
    }

    if len(s.items) >= c.shardCap {
        for k := range s.items {
            delete(s.items, k)
            atomic.AddInt64(&c.count, -1)
            break
        }
    }

    s.items[key] = e
    atomic.AddInt64(&c.count, 1)
    s.mu.Unlock()

    c.sendToPersist(persistenceOp{
        Key: key, Value: val, Exp: exp, RefreshAt: e.refreshAt,
    })
}

func (c *Cache) Delete(key string) {
    s := c.getShard(key)
    s.mu.Lock()
    defer s.mu.Unlock()

    if _, ok := s.items[key]; ok {
        delete(s.items, key)
        atomic.AddInt64(&c.count, -1)
        c.sendToPersist(persistenceOp{Key: key, IsDelete: true})
    }
}

func (c *Cache) sendToPersist(op persistenceOp) {
    // 缓存已关闭则不再接收更新，防止 panic
    if atomic.LoadInt32(&c.closed) == 1 {
        atomic.AddInt64(&c.droppedUpdates, 1)
        return
    }
    select {
    case c.persistCh <- op:
    default:
        atomic.AddInt64(&c.droppedUpdates, 1)
    }
}

// ================= 持久化逻辑 =================

func (c *Cache) StartPersistence(path string) {
    // 设置路径
    c.dbMu.Lock()
    c.dbPath = path
    c.dbMu.Unlock()

    // 预热只读连接 (可选，但推荐)
    if err := c.ensureReadOnlyDB(); err != nil {
        log.Printf("StartPersistence: init roDB failed: %v", err)
        // 注意：这里不 return，依然尝试启动写入协程，保证核心功能可用
    }

    c.wg.Add(1)

    go func() {
        defer c.wg.Done()

        // 写入协程使用独立的连接
        db, err := sql.Open("sqlite", path)
        if err != nil {
            log.Printf("StartPersistence: open db failed: %v", err)
            return
        }
        defer db.Close()

        // 关键性能优化
        db.Exec("PRAGMA journal_mode=WAL;")
        db.Exec("PRAGMA synchronous=NORMAL;")
        
        // 单写原则
        db.SetMaxOpenConns(1)
        db.SetMaxIdleConns(1)

        if err := c.initDB(db); err != nil {
            log.Printf("StartPersistence: initDB failed: %v", err)
            return
        }

        batch := make([]persistenceOp, 0, persistBatchSize)
        ticker := time.NewTicker(persistInterval)
        cleanupTicker := time.NewTicker(cleanupInterval)

        defer ticker.Stop()
        defer cleanupTicker.Stop()

        flush := func() {
            if len(batch) == 0 {
                return
            }
            if err := c.flushBatch(db, batch); err != nil {
                log.Printf("Flush batch failed: %v", err)
            }
            batch = batch[:0]
        }

        cleanExpired := func() {
            now := time.Now().UnixNano()
            _, _ = db.Exec("DELETE FROM ip_cache WHERE exp < ?", now)
        }

        for {
            select {
            case op := <-c.persistCh:
                batch = append(batch, op)
                if len(batch) >= persistBatchSize {
                    flush()
                }
            case <-ticker.C:
                flush()
            case <-cleanupTicker.C:
                cleanExpired()
            case <-c.stop:
                flush()
                return
            }
        }
    }()
}

// ensureReadOnlyDB 线程安全地初始化只读连接 (Double-Check Locking)
func (c *Cache) ensureReadOnlyDB() error {
    // [Fast Fail] 如果缓存已关闭，直接拒绝
    if atomic.LoadInt32(&c.closed) == 1 {
        return fmt.Errorf("cache is closed")
    }
    // 1. 快速检查 (读锁)
    c.dbMu.RLock()
    if c.roDB != nil {
        c.dbMu.RUnlock()
        return nil
    }
    path := c.dbPath
    c.dbMu.RUnlock()

    if path == "" {
        return fmt.Errorf("db path not set")
    }

    // 2. 慢速初始化 (写锁)
    c.dbMu.Lock()
    defer c.dbMu.Unlock()

    // 二次检查
    if c.roDB != nil {
        return nil
    }

    db, err := sql.Open("sqlite", path+"?mode=ro")
    if err != nil {
        return err
    }

    // 只读连接配置
    _, _ = db.Exec("PRAGMA journal_mode=WAL;")
    _, _ = db.Exec("PRAGMA busy_timeout=5000;") // 减少锁竞争报错
    
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)

    c.roDB = db
    return nil
}

func (c *Cache) initDB(db *sql.DB) error {
    _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS ip_cache (
            key TEXT PRIMARY KEY,
            value TEXT,
            exp INTEGER,
            refresh_at INTEGER
        );
        CREATE INDEX IF NOT EXISTS idx_exp ON ip_cache(exp);
    `)
    return err
}

func (c *Cache) flushBatch(db *sql.DB, batch []persistenceOp) error {
    tx, err := db.Begin()
    if err != nil {
        return err
    }

    // 务必检查 Prepare 错误并回滚
    stmtInsert, err := tx.Prepare(
        "INSERT OR REPLACE INTO ip_cache(key, value, exp, refresh_at) VALUES(?, ?, ?, ?)",
    )
    if err != nil {
        _ = tx.Rollback()
        return fmt.Errorf("prepare insert failed: %w", err)
    }
    defer stmtInsert.Close()

    stmtDelete, err := tx.Prepare(
        "DELETE FROM ip_cache WHERE key = ?",
    )
    if err != nil {
        _ = tx.Rollback()
        return fmt.Errorf("prepare delete failed: %w", err)
    }
    defer stmtDelete.Close()

    for _, op := range batch {
        if op.IsDelete {
            _, _ = stmtDelete.Exec(op.Key)
        } else {
            _, _ = stmtInsert.Exec(op.Key, op.Value, op.Exp, op.RefreshAt)
        }
    }

    if err := tx.Commit(); err != nil {
        return fmt.Errorf("commit failed: %w", err)
    }
    return nil
}

// ================= 启动加载 =================

func (c *Cache) LoadFromSQLite(path string) error {
    // 设置路径
    c.dbMu.Lock()
    c.dbPath = path
    c.dbMu.Unlock()

    db, err := sql.Open("sqlite", path)
    if err != nil {
        return err
    }
    defer db.Close()

    // 确保表结构存在
    if err := c.initDB(db); err != nil {
        return err
    }

    now := time.Now().UnixNano()
    rows, err := db.Query(
        "SELECT key, value, exp, refresh_at FROM ip_cache WHERE exp > ?",
        now,
    )
    if err != nil {
        return err
    }
    defer rows.Close()

    for rows.Next() {
        var k, v string
        var exp, refresh int64
        if err := rows.Scan(&k, &v, &exp, &refresh); err == nil {
            c.SetWithTime(k, v, exp, refresh)
        }
    }
    return nil
}

// ================= 只读查询 (统计) =================

func (c *Cache) GetAllItems() (map[string]string, error) {
    return c.GetAllItemsContext(context.Background())
}

func (c *Cache) GetAllItemsContext(ctx context.Context) (map[string]string, error) {
    // 线程安全地获取连接
    if err := c.ensureReadOnlyDB(); err != nil {
        return nil, err
    }

    c.dbMu.RLock()
    db := c.roDB
    c.dbMu.RUnlock()

    if db == nil {
        return nil, fmt.Errorf("db not initialized")
    }

    now := atomic.LoadInt64(&c.now)
    rows, err := db.QueryContext(ctx,
        "SELECT key, value FROM ip_cache WHERE exp > ?",
        now,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    res := make(map[string]string)
    for rows.Next() {
        var k, v string
        if err := rows.Scan(&k, &v); err == nil {
            res[k] = v
        }
    }
    return res, nil
}

// ================= 恢复用辅助方法 =================

func (c *Cache) SetWithTime(key, val string, exp, refreshAt int64) {
    s := c.getShard(key)
    s.mu.Lock()
    defer s.mu.Unlock()

    if _, ok := s.items[key]; ok {
        s.items[key] = entry{val, exp, refreshAt}
        return
    }

    if len(s.items) >= c.shardCap {
        for k := range s.items {
            delete(s.items, k)
            atomic.AddInt64(&c.count, -1)
            break
        }
    }

    s.items[key] = entry{val, exp, refreshAt}
    atomic.AddInt64(&c.count, 1)
}

// ================= 生命周期与后台任务 =================

func (c *Cache) Close() {
    atomic.StoreInt32(&c.closed, 1)
    close(c.stop)
    c.wg.Wait()

    c.dbMu.Lock()
    if c.roDB != nil {
        _ = c.roDB.Close()
        c.roDB = nil
    }
    c.dbMu.Unlock()
}

func (c *Cache) startClock() {
    ticker := time.NewTicker(time.Second)
    c.wg.Add(1)

    go func() {
        defer c.wg.Done()
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

func (c *Cache) startCleanup() {
    ticker := time.NewTicker(time.Minute)
    c.wg.Add(1)

    go func() {
        defer c.wg.Done()
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                now := atomic.LoadInt64(&c.now)
                for i := 0; i < shardCount; i++ {
                    c.cleanupShard(c.shards[i], now)
                    time.Sleep(2 * time.Millisecond)
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
            atomic.AddInt64(&c.count, -1)
        }
    }
}

// ================= 统计 Getter =================

func (c *Cache) Count() int64 {
    return atomic.LoadInt64(&c.count)
}

func (c *Cache) DroppedCount() int64 {
    return atomic.LoadInt64(&c.droppedUpdates)
}