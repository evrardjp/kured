// The node-reboot-detector daemon periodically checks if a reboot is required.
// It is used by node-reboot-reporter to set up node conditions that will eventually trigger node reboots.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/pkg/checkers"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
)

var (
	version = "unreleased"

	rebootSentinelCommand string
	rebootSentinelFile    string
	metricsHost           string
	metricsPort           int
	nodeID                string
	logFormat             string
	period                time.Duration

	rebootRequiredGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "kured",
		Name:      "reboot_required",
		Help:      "OS requires reboot due to software updates.",
	}, []string{"node"})
)

func init() {
	prometheus.MustRegister(rebootRequiredGauge)
}

func main() {

	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.StringVar(&nodeID, "node-id", "", "node name kured runs on, should be passed down from spec.nodeName via KURED_NODE_ID environment variable")
	flag.StringVar(&rebootSentinelCommand, "reboot-sentinel-command", "", "command for which a zero return code will trigger a reboot command")
	flag.StringVar(&rebootSentinelFile, "reboot-sentinel-file", "/sentinel/reboot-required", "path to file whose existence triggers the reboot command")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.DurationVar(&period, "period", time.Minute, "period at which the main operations are done")

	flag.Parse()

	// Load flags from environment variables. Remember the prefix KURED_!
	cli.LoadFromEnv()

	var logger *slog.Logger
	switch logFormat {
	case "json":
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	case "text":
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	default:
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
		logger.Info("incorrect configuration for logFormat, using text handler")
	}
	// For all the old calls using logger
	slog.SetDefault(logger)

	if nodeID == "" {
		slog.Error(fmt.Sprintf("%s_NODE_ID environment variable or --node-id flag required", cli.EnvPrefix))
		os.Exit(1)
	}

	slog.Info("Starting node-reboot-detector",
		"version", version,
		"node", nodeID,
		"rebootPeriod", period,
		"rebootSentinelCommand", rebootSentinelCommand,
		"rebootSentinelFile", rebootSentinelFile,
		"metricsHost", metricsHost,
		"metricsPort", metricsPort,
	)

	rebootChecker, err := checkers.NewRebootChecker(rebootSentinelCommand, rebootSentinelFile)
	if err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to build reboot checker: %v", err))
		os.Exit(4)
	}

	go func() {
		for range time.Tick(period) {
			rebootRequired := rebootChecker.RebootRequired()
			if rebootRequired {
				rebootRequiredGauge.WithLabelValues(nodeID).Set(1)
			} else {
				rebootRequiredGauge.WithLabelValues(nodeID).Set(0)
			}
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
		os.Exit(1)
	} // #nosec G114

}
