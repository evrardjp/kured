// Package main implements daemon periodically checking if a reboot is required.
// It sets up node conditions that will eventually trigger node reboots.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/detectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
)

var (
	version = "unreleased"
)

func main() {
	var (
		// Please continue sorting alphabetically :)
		debug                 bool
		distribution          string
		kubeconfig            string
		logFormat             string
		metricsHost           string
		metricsPort           int
		nodeID                string
		period                time.Duration
		rebootSentinelCommand string
		rebootSentinelFile    string
	)

	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.StringVar(&distribution, "distribution", "custom", "linux distribution running on the node. Used to select appropriate reboot sentinel. Use 'custom' to use custom reboot sentinel command or file.")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.StringVar(&nodeID, "node-id", "", "node name kured runs on, should be passed down from spec.nodeName via KURED_NODE_ID environment variable")
	flag.DurationVar(&period, "period", time.Minute, "period at which a reboot required is detected")
	flag.StringVar(&rebootSentinelCommand, "reboot-sentinel-command", "", "command for which a zero return code will trigger a reboot command")
	flag.StringVar(&rebootSentinelFile, "reboot-sentinel-file", "/sentinel/reboot-required", "path to file whose existence triggers the reboot command")
	flag.Parse()

	// Load flags from environment variables. Remember the prefix KURED_!
	cli.LoadFromEnv()

	// set up signals so we handle the shutdown signal gracefully
	ctx := cli.SetupSignalHandler()

	logger := cli.NewLogger(debug, logFormat)
	// For all the old calls using logger
	slog.SetDefault(logger)

	client := cli.KubernetesClientSetOrDie("", kubeconfig)

	// Core initialisation, error 1 on failure.
	if nodeID == "" {
		slog.Error(fmt.Sprintf("%s_NODE_ID environment variable or --node-id flag required", cli.EnvPrefix))
		os.Exit(1)
	}

	slog.Info("Starting Kubernetes Node Reboot Detector",
		"version", version,
		"rebootPeriod", period,
		"metricsHost", metricsHost,
		"metricsPort", metricsPort,
		"debug", debug,

		"node", nodeID,
		"distribution", distribution,
	)

	// TODO: Add distribution-based defaulting for rebootSentinelCommand and rebootSentinelFile
	rebootChecker, err := detectors.NewRebootChecker(rebootSentinelCommand, rebootSentinelFile)
	if err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to build reboot checker: %v", err))
		os.Exit(4)
	}

	controllerConfig := &detectors.Config{
		Period:         period,
		RebootDetector: rebootChecker,
		NodeID:         nodeID,
		Client:         client,
	}
	go detectors.MaintainRebootRequiredCondition(ctx, controllerConfig)

	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
		os.Exit(1)
	} // #nosec G114

}
