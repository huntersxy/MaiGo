package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"maibot-go/internal/common/utils"

	"go.uber.org/zap"
)

// startAPIServer 启动新 maim_message API Server（独立端口与路径）
func (s *Server) startAPIServer(ctx context.Context) error {
	cfg := s.config()
	mux := http.NewServeMux()
	path := trimSpace(cfg.APIPath)
	if path == "" {
		path = defaultAPIPath
	}
	mux.HandleFunc(path, s.handleAPIWS)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"service":"maim_message_api","ws":"` + path + `"}`))
	})

	listener, err := net.Listen("tcp", cfg.APIAddr())
	if err != nil {
		// 对齐 Python port_checker.py:63-84：端口占用时给出可操作的排查建议
		if utils.IsPortConflictError(err) {
			host, port := splitHostPort(cfg.APIAddr())
			zap.L().Error(utils.BuildPortConflictMessage("maim_message API 服务", host, port))
			for _, hint := range utils.PortConflictHints(port, "maim_message.api_port") {
				zap.L().Error(hint)
			}
		}
		return err
	}
	serveListener := net.Listener(listener)
	if cfg.APISSLEnabled {
		tlsConfig, err := buildAPITLSConfig(cfg.APISSLConfig)
		if err != nil {
			_ = listener.Close()
			return err
		}
		serveListener = tls.NewListener(listener, tlsConfig)
	}
	s.apiServer = &http.Server{
		Addr:              cfg.APIAddr(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.apiServer != nil {
			_ = s.apiServer.Shutdown(shutdownCtx)
		}
	}()
	go func() {
		err := s.apiServer.Serve(serveListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			zap.L().Error("maim_message API Server 退出", zap.Error(err))
		}
	}()
	zap.L().Info("maim_message API Server 启动",
		zap.String("addr", cfg.APIAddr()),
		zap.String("path", path),
		zap.Bool("api_key_required", cfg.APIKeyRequired()),
		zap.Bool("ssl_enabled", cfg.APISSLEnabled))
	return nil
}

func buildAPITLSConfig(cfg TLSConfig) (*tls.Config, error) {
	return buildServerTLSConfig(cfg, "maim_message API SSL")
}

func buildServerTLSConfig(cfg TLSConfig, label string) (*tls.Config, error) {
	if !cfg.Enabled() {
		return nil, fmt.Errorf("%s 已启用但缺少 cert_file/key_file", label)
	}
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 %s 证书失败: %w", label, err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("读取 %s CA 失败: %w", label, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s CA 不包含有效证书", label)
		}
		tlsConfig.ClientCAs = pool
		if cfg.VerifyPeer {
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
		}
	} else if cfg.VerifyPeer {
		return nil, fmt.Errorf("%s 要求校验客户端证书但未配置 ca_certs", label)
	}
	return tlsConfig, nil
}
