# IP Resolver (IP 归属地解析器)

本项目是专为 [mosdns-x](https://github.com/BaeKey/mosdns-x) 开发的配套 IP 打标系统。它通过为 IP 生成包含“省份+运营商”信息的缓存 Key，助力 `mosdns-x` 实现基于地理位置和运营商的精细化 DNS 缓存策略（即让同一地区的相同运营商用户共享 DNS 缓存）。

系统对接了**腾讯云市场**的 IP 查询归属地接口（支持多家供应商），并实现了高性能的本地缓存机制。

## 主要功能

*   **MosDNS-X 配套**: 核心设计目标是为 `mosdns-x` 提供高效的 IP -> Location/ISP 映射。
*   **多级缓存架构**:
    *   **内存缓存**: 高速响应热点请求。
    *   **持久化存储**: 使用 SQLite (`.cache.db`) 保存缓存数据，重启不丢失。
    *   **智能过期**: 支持 TTL 设置（默认 30 天），且具备**预刷新机制**（在缓存即将过期时自动后台刷新），确保热点数据始终最新。
*   **并发控制**:
    *   **请求去重 (Singleflight)**: 防止高并发下同一 IP 重复请求上游接口（缓存击穿防护）。
    *   **Worker 池**: 控制上游 API 的并发请求数，避免触发流控。
*   **配额管理**: 内置腾讯云 API 调用配额监控，防止超额使用。
*   **双协议支持**: 支持 TCP 和 Unix Domain Socket (UDS) 监听。
*   **完善监控**: 提供详细的缓存命中率、Tag 统计和系统状态接口。

## 配置说明

使用 YAML 格式配置文件 (默认 `config.yaml`)。

```yaml
# 业务监听地址
listen_addr: "unix:///var/run/ip-resolver.sock" # 或 TCP: "0.0.0.0:8080"

# 监控接口地址 (仅支持 TCP)
monitor_addr: "0.0.0.0:9090"

# 缓存策略
cache_refresh_ratio: 10          # 在 TTL 最后 10% 时间段内触发预刷新
cache_ttl_seconds: 2592000       # 缓存有效期 30 天
cache_store_path: "./.cache.db"  # SQLite 缓存文件路径

# 日志设置
log_level: "info"
log_file: "./resolver.log"

# 上游供应商配置
provider:
  name: "38599"                  # 供应商 ID (如数脉 38599)
  secret_id: "your_secret_id"    # 对应云市场购买后的 SecretId
  secret_key: "your_secret_key"  # 对应云市场购买后的 SecretKey

# 腾讯云账号（用于查询剩余配额）
quota:
  instance_id: "market-xxxx"       # 云市场实例 ID
  secret_id: "tencent_cloud_id"    # 腾讯云账号 SecretId
  secret_key: "tencent_cloud_key"  # 腾讯云账号 SecretKey
```

## 快速开始

### 环境要求
*   Go 1.25+

### 安装步骤

1.  克隆仓库:
    ```bash
    git clone https://github.com/BaeKey/ip-resolver.git
    cd ip-resolver
    ```

2.  编译项目:
    ```bash
    go mod download
    go build -o ip-resolver cmd/server/main.go
    ```

3.  运行服务:
    *   创建 `config.yaml` 并填入你的 API 密钥。
    *   启动:
        ```bash
        ./ip-resolver -c config.yaml
        ```

## API 使用指南

### 查询 IP 归属地 (Resolve)

**协议**: HTTP over TCP / Unix Socket

**接口**: `GET /<ip_address>`

**响应**:
*   **200 OK**: 返回纯文本的 `省份 运营商` (例如: `beijing_cmcc`)。
*   **202 Accepted**: 请求已接收正在处理中（通常在缓存预热或冷启动时），请稍后重试。
*   **400 Bad Request**: IP 格式错误。
*   **429 Too Many Requests**: 系统繁忙。

**示例**:
```bash
curl --unix-socket /var/run/ip-resolver.sock http://localhost/1.1.1.1
# 输出: beijing_cmcc
```

### 监控统计 (Monitoring)

**接口**: `GET http://<monitor_addr>/statistics`
*   返回 HTML 页面，包含缓存总数、丢弃计数、Tag 命中分布等详细信息。

**接口**: `GET http://<monitor_addr>/status`
*   返回简单的健康检查状态。
