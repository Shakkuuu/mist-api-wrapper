package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	defaultListenAddr = ":8080"
	envBaseURL        = "MIST_BASE_URL"
	envToken          = "MIST_API_TOKEN"
	envListenAddr     = "MIST_LISTEN_ADDR"
)

func main() {
	baseURL := strings.TrimSpace(os.Getenv(envBaseURL))
	token := strings.TrimSpace(os.Getenv(envToken))
	listenAddr := strings.TrimSpace(os.Getenv(envListenAddr))
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}

	if baseURL == "" || token == "" {
		log.Fatalf("環境変数 %s と %s を設定してください", envBaseURL, envToken)
	}

	target, err := url.Parse(baseURL)
	if err != nil {
		log.Fatalf("ベースURLの解析に失敗しました: %v", err)
	}

	proxy := newMistProxy(target, token)

	mux := http.NewServeMux()
	mux.Handle("/health", http.HandlerFunc(healthHandler))
	mux.Handle("/", proxy)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           loggingMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Printf("サーバー停止処理に失敗しました: %v", err)
		}
		close(idleConnsClosed)
	}()

	log.Printf("Mist API プロキシを %s で待ち受け (base: %s)", listenAddr, target.Redacted())
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("サーバー起動に失敗しました: %v", err)
	}

	<-idleConnsClosed
	log.Println("サーバーを停止しました")
}

func newMistProxy(target *url.URL, token string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director

	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		req.Header.Set("Authorization", fmt.Sprintf("Token %s", token))
		req.Header.Set("Accept", "application/json")
		stripHopHeaders(req.Header)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		stripHopHeaders(resp.Header)
		return nil
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		log.Printf("プロキシエラー: %v", err)
		http.Error(rw, "アップストリームへの接続に失敗しました", http.StatusBadGateway)
	}

	return proxy
}

func stripHopHeaders(header http.Header) {
	for _, h := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(h)
	}
}

func healthHandler(rw http.ResponseWriter, _ *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write([]byte(`{"status":"ok"}`))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		start := time.Now()
		remoteIP, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			remoteIP = req.RemoteAddr
		}

		lrw := &loggingResponseWriter{ResponseWriter: rw, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, req)

		log.Printf("%s %s %d %s from %s", req.Method, req.URL.String(), lrw.statusCode, time.Since(start), remoteIP)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}
