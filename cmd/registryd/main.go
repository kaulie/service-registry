// Command registryd 是全局独立的服务注册中心（元信息存储中心）。
//
//	registryd            # 读环境变量，默认监听 127.0.0.1:4240
//
// 注意：本服务**不探活、不心跳、不启停任何进程**——它只负责把服务契约
// （含对外 API 这一基础属性）与实例元信息持久化，供所有平台拉取。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaulie/service-registry/internal/api"
	"github.com/kaulie/service-registry/internal/config"
	"github.com/kaulie/service-registry/internal/metrics"
	"github.com/kaulie/service-registry/internal/store"
)

// version 由构建脚本通过 -ldflags "-X main.version=<8 位短 hash>" 注入。
var version = "dev"

func main() {
	log := newLogger()

	cfg, err := config.Load()
	if err != nil {
		log.Error("配置非法", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("打开数据库失败", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if cfg.DefaultNamespace != "" {
		created, err := st.EnsureNamespace(ctx, cfg.DefaultNamespace, "默认命名空间（启动时自动播种）")
		if err != nil {
			cancel()
			log.Error("播种默认命名空间失败", "err", err)
			os.Exit(1)
		}
		if created {
			log.Info("已播种默认命名空间", "namespace", cfg.DefaultNamespace)
		}
	}
	cancel()

	m := metrics.New()
	declareMetrics(m)
	srv := api.NewServer(st, cfg, m, log, version)

	if cfg.WriteAuthOpen {
		// 刻意默认、但必须显眼：开放的写接口意味着任何人都能往唯一的元信息真源里写数据。
		log.Warn("写接口未鉴权（REGISTRY_WRITE_AUTH=open）：任何人只要连得上就能登记/修改/删除服务元信息；" +
			"需要收紧时设 REGISTRY_WRITE_AUTH=token 并重启")
	}
	if cfg.ReadAuthRequired {
		log.Info("读接口要求令牌（REGISTRY_READ_AUTH=token）：各平台拉取时需带令牌")
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout 必须为 0：SSE 与 long-poll 是长连接，一旦设置会被掐断。
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	// 审计/变更记录的保留策略（默认 0 = 永久保留）。
	stopPrune := startPruneLoop(log, st, cfg.ChangeRetentionDays)
	defer stopPrune()

	errc := make(chan error, 1)
	go func() {
		log.Info("service-registry 启动",
			"addr", cfg.Addr(), "portFrom", cfg.PortSource, "db", cfg.DBPath, "version", version,
			"readAuthRequired", cfg.ReadAuthRequired, "defaultNamespace", cfg.DefaultNamespace)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errc:
		log.Error("HTTP 服务异常退出", "err", err)
		os.Exit(1)
	case sig := <-sigc:
		log.Info("收到退出信号，开始优雅关闭", "signal", sig.String())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("优雅关闭失败", "err", err)
	}
	log.Info("已退出")
}

// startPruneLoop 每日清理超期的变更/审计记录（0 天 = 不清理，直接返回空函数）。
func startPruneLoop(log *slog.Logger, st *store.Store, days int) func() {
	if days <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().AddDate(0, 0, -days)
				n, err := st.PruneChanges(ctx, cutoff)
				if err != nil {
					log.Warn("清理变更记录失败", "err", err)
					continue
				}
				if n > 0 {
					log.Info("已清理超期变更记录", "rows", n, "before", cutoff)
				}
			}
		}
	}()
	return cancel
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch os.Getenv("REGISTRY_LOG_LEVEL") {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func declareMetrics(m *metrics.Registry) {
	m.Counter("registry_http_requests_total", "HTTP 请求数（按路由/方法/状态码）")
	m.Counter("registry_errors_total", "5xx 错误数（按错误码）")
	m.Gauge("registry_build_info", "构建信息（恒为 1，版本在标签里）")
	m.Gauge("registry_uptime_seconds", "进程运行时长（秒）")
	m.Gauge("registry_namespaces", "命名空间数")
	m.Gauge("registry_services", "服务契约数")
	m.Gauge("registry_instances", "登记的实例数")
	m.Gauge("registry_endpoints", "端点索引数")
	m.Gauge("registry_changes_total", "变更/审计记录数")
	m.Gauge("registry_revision", "当前全局 revision")
}
