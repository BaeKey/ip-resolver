# IP Resolver

High-performance IP address resolution service with multi-level caching and multi-provider support. Designed to resolve IP addresses to physical locations (Province + ISP) efficiently using Tencent Cloud Market APIs.

## Features

-   **Multi-Provider Architecture**: Extensible provider interface supporting multiple upstream services (e.g., Tencent Cloud Market ShuMai 38599).
-   **Smart Caching**:
    -   **Local Cache**: High-speed memory cache with SQLite persistence (`.cache.db`).
    -   **TTL Management**: Configurable expiration (default 30 days).
    -   **Pre-refresh**: Probabilistic background refresh mechanism to update hot keys before they expire.
-   **Concurrency Control**:
    -   **Inflight Dedup**: preventing "thundering herd" problems by merging duplicate requests for the same IP.
    -   **Worker Pools**: Configurable concurrency for upstream fetches.
-   **Quota Management**: Built-in Tencent Cloud API quota monitoring to prevent over-usage.
-   **Monitoring & Observability**:
    -   Real-time cache statistics and hit rates.
    -   Detailed status endpoints.
-   **Dual Protocol Support**: Listen on TCP or Unix Domain Sockets.

## Configuration

The service is configured via a YAML file (default: `config.yaml`).

```yaml
# Service Listener
listen_addr: "unix:///var/run/ip-resolver.sock" # or "0.0.0.0:8080" for TCP

# Monitoring Listener (TCP only)
monitor_addr: "0.0.0.0:9090"

# Caching Strategy
cache_refresh_ratio: 10          # Pre-refresh in the last 10% of TTL
cache_ttl_seconds: 2592000       # 30 days
cache_store_path: "./.cache.db"  # Path to SQLite cache file

# Logging
log_level: "info"
log_file: "./resolver.log"

# Provider Settings
provider:
  name: "38599"                  # Provider ID (e.g., ShuMai)
  secret_id: "your_secret_id"
  secret_key: "your_secret_key"

# Quota Settings (Optional)
quota:
  instance_id: "market-xxxx"
  secret_id: "tencent_cloud_secret_id"
  secret_key: "tencent_cloud_secret_key"
```

## Getting Started

### Prerequisites

-   Go 1.25+

### Installation

1.  Clone the repository:
    ```bash
    git clone https://github.com/yourusername/ip-resolver.git
    cd ip-resolver
    ```

2.  Build the project:
    ```bash
    go mod download
    go build -o ip-resolver cmd/server/main.go
    ```

### Running the Service

1.  Create a `config.yaml` file with your credentials.
2.  Run the server:
    ```bash
    ./ip-resolver -c config.yaml
    ```

## API Usage

### Resolve IP

**Protocol**: HTTP over TCP or Unix Socket

**Endpoint**: `GET /<ip_address>`

**Response**:
-   **200 OK**: Returns plain text Location and ISP (e.g., `广东省 电信`).
-   **202 Accepted**: Request accepted and processing (warm-up). Retry later.
-   **400 Bad Request**: Invalid IP format.
-   **429 Too Many Requests**: System busy.

**Example**:
```bash
curl --unix-socket /var/run/ip-resolver.sock http://localhost/1.1.1.1
# Output: 广东省 电信
```

### Monitoring

**Endpoint**: `GET http://<monitor_addr>/statistics`

Returns an HTML page with:
-   Total cached items count.
-   Dropped updates count (disk pressure indicator).
-   Detailed list of cached tags and IP ranges.

**Endpoint**: `GET http://<monitor_addr>/status`

Returns simple status checks.
