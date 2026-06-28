package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/elazarl/goproxy"
	"golang.org/x/time/rate"
	"log/slog"
)

const bytesPerSec = 131072 // 1 Mb/s per connection (128 KB/s)

// Start runs an HTTP forward proxy with logging and per-connection bandwidth limits.
func Start(port int) {
	p := goproxy.NewProxyHttpServer()
	p.Logger = slogWriter{}
	p.OnResponse().DoFunc(func(r *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if r.Body != nil {
			r.Body = &limitedReadCloser{r.Body, rate.NewLimiter(rate.Limit(bytesPerSec), bytesPerSec)}
		}
		return r
	})
	slog.Info("proxy listening", "port", port)
	//nolint:gosec // proxy is internal
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), p); err != nil {
		slog.Error("proxy serve error", "err", err)
	}
}

type slogWriter struct{}

func (w slogWriter) Printf(format string, args ...any) {
	slog.Debug(fmt.Sprintf(format, args...))
}

type limitedReadCloser struct {
	rc  io.ReadCloser
	lim *rate.Limiter
}

func (l *limitedReadCloser) Read(p []byte) (int, error) {
	n, err := l.rc.Read(p)
	if n > 0 {
			//nolint:errcheck,gosec // best-effort rate limit
		l.lim.WaitN(context.Background(), n)  //nolint:errcheck
	}
	return n, err
}

func (l *limitedReadCloser) Close() error { return l.rc.Close() }
