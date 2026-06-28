package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/elazarl/goproxy"
	"golang.org/x/time/rate"
	"k8s.io/klog/v2"
)

const bytesPerSec = 131072 // 1 Mb/s per connection (128 KB/s)

// Start runs an HTTP forward proxy with logging and per-connection bandwidth limits.
func Start(port int) {
	p := goproxy.NewProxyHttpServer()
	p.Logger = klogWriter{}
	p.OnResponse().DoFunc(func(r *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if r.Body != nil {
			r.Body = &limitedReadCloser{r.Body, rate.NewLimiter(rate.Limit(bytesPerSec), bytesPerSec)}
		}
		return r
	})
	klog.InfoS("proxy listening", "port", port)
	//nolint:gosec // proxy is internal
	klog.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), p))
}

type klogWriter struct{}

func (w klogWriter) Printf(format string, args ...any) {
	klog.V(2).Infof(format, args...)
}

type limitedReadCloser struct {
	rc  io.ReadCloser
	lim *rate.Limiter
}

func (l *limitedReadCloser) Read(p []byte) (int, error) {
	n, err := l.rc.Read(p)
	if n > 0 {
			//nolint:errcheck,gosec // best-effort rate limit
		l.lim.WaitN(context.Background(), n)
	}
	return n, err
}

func (l *limitedReadCloser) Close() error { return l.rc.Close() }
