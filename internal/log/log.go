package log

import (
	"flag"

	"github.com/breakfix/breakfix/internal/build"
	"k8s.io/klog/v2"
)

func Init() {
	klog.InitFlags(nil)
	if build.IsDev() {
		// dev: default to verbose
		_ = flag.Set("v", "2")
	} else {
		_ = flag.Set("v", "0")
	}
}
