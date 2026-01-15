package main

import (
	"context"
	"errors"
	"flag"
	"ip-resolver/internal/config"
	"ip-resolver/internal/monitor"
	"ip-resolver/internal/provider"
	"ip-resolver/internal/worker"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	// 1. 解析配置
	configPath := flag.String("c", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	// 1.1 日志配置
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("无法打开日志文件 %s: %v, 将仅输出到控制台", cfg.LogFile, err)
		} else {
			// 同时输出到控制台和文件
			mw := io.MultiWriter(os.Stdout, f)
			log.SetOutput(mw)
		}
	}

	log.Printf(
		"启动 ip-resolver | API: %s | 监控: %s | 日志等级: %s",
		cfg.ListenAddr,
		cfg.MonitorAddr,
		cfg.LogLevel,
	)

	// 2. 初始化组件
	mon := monitor.New()

	prov, err := provider.NewProviderByName(
		cfg.Provider.Name,
		cfg.Provider.SecretID,
		cfg.Provider.SecretKey,
		mon,
	)
	if err != nil {
		log.Fatalf("Provider 初始化失败: %v", err)
	}
	log.Printf("使用 IP 提供商: %s", prov.Name())

	if cfg.Quota.InstanceID != "" {
        log.Printf("[初始化] 启用配额检查, 实例ID: %s", cfg.Quota.InstanceID)
		
		// 对应 config.yaml 中的 quota 配置
		quotaChecker := provider.NewTencentQuotaChecker(
			cfg.Quota.SecretID,
			cfg.Quota.SecretKey,
			cfg.Quota.InstanceID,
		)

		mon.SetQuotaFetcher(quotaChecker.GetRemainingRequests)
	} else {
		log.Println("[初始化] 配额检查未启用")
	}

	mgr := worker.NewManager(prov, cfg)
	
	mon.SetCacheFetcher(mgr.GetCacheCount)

	// 3. 信号处理
	rootCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	// 4. 启动后台任务
	mgr.Start()

	// 5. API Server (TCP / Unix Socket)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/", mgr.HandleUpdate)

	apiSrv := &http.Server{
		Handler:           apiMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	apiListener, apiCleanup, err := createListener(cfg.ListenAddr)
	if err != nil {
		log.Fatalf("无法创建 API 监听器: %v", err)
	}
	defer apiCleanup()

	// 6. 监控 Server (仅 TCP)
	monMux := http.NewServeMux()
	monMux.HandleFunc("/status", mon.HandleStatus)

	monSrv := &http.Server{
		Addr:              cfg.MonitorAddr,
		Handler:           monMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// 7. 启动 Server
	errCh := make(chan error, 2)

	go func() {
		log.Printf("API server 监听于 %s", cfg.ListenAddr)
		if err := apiSrv.Serve(apiListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	go func() {
		log.Printf("监控 server 监听于 %s", cfg.MonitorAddr)
		if err := monSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// 8. 等待退出信号
	select {
	case <-rootCtx.Done():
		log.Println("收到退出信号")
	case err := <-errCh:
		log.Printf("Server 错误: %v", err)
		stop()
	}

	log.Println("正在关闭...")

	// 先关闭 HTTP Server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := apiSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("API关闭失败: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		if err := monSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("监控关闭失败: %v", err)
		}
	}()

	wg.Wait()

	// 确认无流量后关闭 Manager
	mgr.Stop() 
	log.Println("退出完成")
}

// createListener 创建 TCP 或 Unix Socket 监听器
func createListener(addr string) (net.Listener, func(), error) {
	// Unix Socket
	if strings.HasPrefix(addr, "unix://") {
		socketPath := strings.TrimPrefix(addr, "unix://")

		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}

		l, err := net.Listen("unix", socketPath)
		if err != nil {
			return nil, nil, err
		}

		if err := os.Chmod(socketPath, 0660); err != nil {
			l.Close()
			return nil, nil, err
		}

		cleanup := func() {
			_ = l.Close()
			_ = os.Remove(socketPath)
		}

		return l, cleanup, nil
	}

	// TCP
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}

	return l, func() { _ = l.Close() }, nil
}
