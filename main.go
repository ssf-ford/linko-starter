package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	"errors"
	"fmt"
	"bufio"
	pkgerr "github.com/pkg/errors"

	"boot.dev/linko/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	logger, closeFunc, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		return 1
	}
	defer func() {
		if err := closeFunc(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close logger: %v\n", err)
		}
	}()

	st, err := store.New(dataDir, logger)
	if err != nil {
		//logger.Error(fmt.Sprintf("failed to create store: %v", err))
		logger.Error("failed to create store", "error", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		//logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		logger.Error("failed to shutdown server", "error", err)
			
		return 1
	}
	if serverErr != nil {
		//logger.Error(fmt.Sprintf("server error: %v", serverErr))
		logger.Error("server error", "error", serverErr)
			
		return 1
	}
	if err := closeFunc(); err != nil {
		//logger.Info(fmt.Sprintf("failed to close logger: %v", err))
		logger.Info("failed to close logger", "error", err)

		return 1
	}
	return 0
}

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace

}
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}

		if stackError, ok := errors.AsType[stackTracer](err); ok {
			return slog.GroupAttrs("error", slog.Attr{
				Key: "message",
				Value: slog.StringValue(stackError.Error()),
			}, slog.Attr{
				Key: "stack_trace",
				Value: slog.StringValue(fmt.Sprintf("%+v", stackError.StackTrace())),
			})
		}
	}
	return a
}

func initializeLogger(logFilename string) (*slog.Logger, closeFunc, error) {

	handlers := []slog.Handler{
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			ReplaceAttr: replaceAttr,
		}),
	}
	closers := []closeFunc{}

	if logFilename != "" {
		file, err := os.OpenFile(logFilename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open log file: %v", err)
		}
		buffer := bufio.NewWriterSize(file, 8192)
		close := func() error {
			if err := buffer.Flush(); err != nil {
				return fmt.Errorf("failed to flush log file: %w", err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("failed to close log file: %w", err)
			}
			return nil
		}
		handlers = append(handlers, slog.NewJSONHandler(buffer, &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		}))
		closers = append(closers, close)
	}
	closer := func() error {
		var errs []error
		for _, close := range closers {
			if err := close(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	return slog.New(slog.NewMultiHandler(handlers...)), closer, nil
}

