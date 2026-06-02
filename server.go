package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"boot.dev/linko/internal/store"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

type LogContext struct {
	Username string
	Error    error
}

func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if logCtx, ok := ctx.Value(LogContextKey).(*LogContext); ok {
		logCtx.Error = err
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError:
		http.Error(w, http.StatusText(status), status)
	default:
		http.Error(w, err.Error(), status)
	}
}

type spyReaderCloser struct {
	io.ReadCloser
	bytesRead int
}

func (r *spyReaderCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}

	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logContext := LogContext{}

			spyReader := &spyReaderCloser{ReadCloser: r.Body}
			r.Body = spyReader
			r = r.WithContext(context.WithValue(r.Context(), LogContextKey, &logContext))

			spyWriter := &spyResponseWriter{ResponseWriter: w}

			start := time.Now()
			next.ServeHTTP(spyWriter, r)
			//logger.Info(fmt.Sprintf("Served request: %s %s", r.Method, r.URL.Path))
			logAttrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", redactIP(r.RemoteAddr)),
				slog.Int("duration", int(time.Since(start).Milliseconds())),
				slog.Int("request_body_bytes", spyReader.bytesRead),
				slog.Int("response_status", spyWriter.statusCode),
				slog.Int("response_body_bytes", spyWriter.bytesWritten),
			}

			if username := logContext.Username; username != "" {
				logAttrs = append(logAttrs, slog.String("user", username))
			}

			if err := logContext.Error; err != nil {
				logAttrs = append(logAttrs, slog.Any("error", err))
			}

			if requestId := r.Header.Get("X-Request-ID"); requestId != "" {
				logAttrs = append(logAttrs, slog.String("request_id", requestId))
			}

			logger.Info("Served request", logAttrs...)
		})
	}
}

func requestIdHandler() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Request-ID") == "" {
				r.Header.Set("X-Request-ID", rand.Text())
			}
			w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))
			next.ServeHTTP(w, r)
		})
	}
}

func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: requestIdHandler()(requestLogger(logger)(mux)),
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger:     logger,
	}

	mux.HandleFunc("GET /", s.handlerIndex)
	mux.Handle("POST /api/login", s.authMiddleware(http.HandlerFunc(s.handlerLogin)))
	mux.Handle("POST /api/shorten", s.authMiddleware(http.HandlerFunc(s.handlerShortenLink)))
	mux.Handle("GET /api/stats", s.authMiddleware(http.HandlerFunc(s.handlerStats)))
	mux.Handle("GET /api/urls", s.authMiddleware(http.HandlerFunc(s.handlerListURLs)))
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	mux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)

	return s
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	s.logger.Debug(fmt.Sprintf("Linko is running on http://localhost%s", s.httpServer.Addr))
	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	s.logger.Debug("Linko is shutting down")
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}

func redactIP(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return hostport
	}
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("%d.%d.%d.x", ip4[0], ip4[1], ip4[2])
	}
	return ip.String()
}
