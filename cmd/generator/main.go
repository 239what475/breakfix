// Command generator produces breakfix challenges from a topic.
// It runs the 3-phase agent workflow: Generate → Judge → Verify.
package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/klog/v2"

	"github.com/breakfix/breakfix/internal/generator"
)

func main() {
	topic := flag.String("topic", "", "Challenge topic")
	outputDir := flag.String("output", "data/challenges", "Output directory")
	klog.InitFlags(nil)
	flag.Parse()

	if *topic == "" {
		*topic = os.Getenv("TOPIC")
	}
	if *topic == "" {
		klog.Fatal("--topic is required")
	}

	g := &generator.Generator{
		Topic:      *topic,
		OutputDir:  *outputDir,
		Registry:   envOr("REGISTRY", "localhost:5000"),
		ACRNS:      envOr("ACR_NAMESPACE", "break-fix"),
		Kubeconfig: os.Getenv("KUBECONFIG"),
	}

	if err := g.Run(context.Background()); err != nil {
		klog.Fatal(err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
