// Package main implements a Kubernetes reboot inhibitor that prevents node reboots
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/inhibitors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
)

var (
	version = "unreleased"
)

func main() {
	var (
		// Please continue sorting alphabetically :)
		alertFilter          cli.RegexpValue
		alertFilterMatchOnly bool
		alertFiringOnly      bool
		blockingPodSelectors []string
		debug                bool
		kubeconfig           string
		logFormat            string
		metricsHost          string
		metricsPort          int
		period               time.Duration
		prometheusURL        string
	)
	flag.Var(&alertFilter, "alert-filter-regexp", "alert names to ignore when checking for active alerts")
	flag.BoolVar(&alertFilterMatchOnly, "alert-filter-match-only", false, "Only block if the alert-filter-regexp matches active alerts")
	flag.BoolVar(&alertFiringOnly, "alert-firing-only", false, "only consider firing alerts when checking for active alerts")
	flag.StringArrayVar(&blockingPodSelectors, "blocking-pod-selector", []string{}, "label selector identifying pods whose presence should prevent reboots (can be repeated multiple times)")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")

	flag.DurationVar(&period, "period", time.Minute, "reboot-inhibitor tick period")
	flag.StringVar(&prometheusURL, "prometheus-url", "", "Prometheus instance to probe for active alerts")

	flag.Parse()

	// Load flags from environment variables (Keep in mind: This does not work with blockingPodSelectors, as it is an array var)
	cli.LoadFromEnv()

	// set up signals so we handle the shutdown signal gracefully
	ctx := cli.SetupSignalHandler()

	logger := cli.NewLogger(debug, logFormat)
	// For all the old calls using logger
	slog.SetDefault(logger)

	client := cli.KubernetesClientSetOrDie("", kubeconfig)

	slog.Info("Starting Kubernetes Reboot Inhibitor",
		"version", version,
		"period", period,
		"metricsHost", metricsHost,
		"metricsPort", metricsPort,
		"debug", debug,

		"alertFilter", alertFilter.String(),
		"alertFiringOnly", alertFiringOnly,
		"alertFilterMatchOnly", alertFilterMatchOnly,
		"blockingPodSelectors", blockingPodSelectors,
	)

	ticker := time.NewTicker(period)

	config, errConfig := inhibitors.NewControllerConfig(
		ticker,
		client,
		logger,
		blockingPodSelectors,
		prometheusURL,
		alertFilter.Regexp,
		alertFiringOnly,
		alertFilterMatchOnly,
		period,
	)
	if errConfig != nil {
		slog.Error("Failed to create controller config", "error", errConfig)
		os.Exit(1)
	}
	go inhibitors.MaintainCondition(ctx, config)
	// Use promhttp.Handler() with timeout configuration
	handler := promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer,
		promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
			Timeout: 5 * time.Second,
		}),
	)

	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", metricsHost, metricsPort),
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		logger.Error("Failed to start metrics server", "error", err)
		os.Exit(9)
	}
}
