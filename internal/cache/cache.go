package cache

import (
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

    // 统计
    count          int64
    droppedUpdates int64

    now int64

    stop      chan struct{}
    persistCh chan persistenceOp

    dbPath string

    // === 新增 ===
    roDB *sql.DB
    wg   sync.WaitGroup
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

// ================= 核心读写 =================

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
    select {
    case c.persistCh <- op:
    default:
        atomic.AddInt64(&c.droppedUpdates, 1)
    }
}

// ================= 持久化 =================

func (c *Cache) StartPersistence(path string) {
    c.dbPath = path

    if err := c.initReadOnlyDB(path); err != nil {
        log.Printf("初始化只读 DB 失败: %v", err)
        return
    }

    c.wg.Add(1)

    go func() {
        defer c.wg.Done()

        db, err := sql.Open("sqlite", path)
        if err != nil {
            log.Printf("持久化启动失败: %v", err)
            return
        }
        defer db.Close()

        db.Exec("PRAGMA journal_mode=WAL;")
        db.Exec("PRAGMA synchronous=NORMAL;")

        db.SetMaxOpenConns(1)
        db.SetMaxIdleConns(1)

        if err := c.initDB(db); err != nil {
            log.Printf("初始化数据库失败: %v", err)
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
                log.Printf("批量写入失败: %v", err)
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

func (c *Cache) initReadOnlyDB(path string) error {
    db, err := sql.Open("sqlite", path)
    if err != nil {
        return err
    }
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

    stmtInsert, _ := tx.Prepare(
        "INSERT OR REPLACE INTO ip_cache(key, value, exp, refresh_at) VALUES(?, ?, ?, ?)",
    )
    stmtDelete, _ := tx.Prepare(
        "DELETE FROM ip_cache WHERE key = ?",
    )

    for _, op := range batch {
        if op.IsDelete {
            _, _ = stmtDelete.Exec(op.Key)
        } else {
            _, _ = stmtInsert.Exec(op.Key, op.Value, op.Exp, op.RefreshAt)
        }
    }

    stmtInsert.Close()
    stmtDelete.Close()

    return tx.Commit()
}

// ================= 启动加载 =================

func (c *Cache) LoadFromSQLite(path string) error {
    c.dbPath = path

    db, err := sql.Open("sqlite", path)
    if err != nil {
        return err
    }
    defer db.Close()

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

// ================= 只读查询 =================

func (c *Cache) GetAllItems() (map[string]string, error) {
    if c.roDB == nil {
        return nil, fmt.Errorf("persistence not enabled")
    }

    now := atomic.LoadInt64(&c.now)

    rows, err := c.roDB.Query(
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

// ================= 直接写入（恢复用） =================

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

// ================= 生命周期 =================

func (c *Cache) Close() {
    close(c.stop)
    c.wg.Wait()

    if c.roDB != nil {
        _ = c.roDB.Close()
    }
}

// ================= 后台任务 =================

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

// ================= 统计方法 =================

// Count 返回当前内存中缓存的条目总数
// 使用原子操作保证并发安全，且性能极高
func (c *Cache) Count() int64 {
    return atomic.LoadInt64(&c.count)
}

// DroppedCount 返回因持久化队列满而丢失的更新数量
func (c *Cache) DroppedCount() int64 {
    return atomic.LoadInt64(&c.droppedUpdates)
}