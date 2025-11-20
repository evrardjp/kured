package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	papi "github.com/prometheus/client_golang/api"
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

	// Core initialisation, error 1 on failure
	promClient, err := papi.NewClient(papi.Config{Address: prometheusURL})
	if err != nil {
		slog.Info("Failed to create Prometheus client", "error", err.Error())
		os.Exit(1)
	}

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
	go InhibitorsWatchLoop(ctx, ticker, blockingPodSelectors, client, prometheusURL, promClient, alertFilter.Regexp, alertFiringOnly, alertFilterMatchOnly, period)
	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil)) // #nosec G114
}
