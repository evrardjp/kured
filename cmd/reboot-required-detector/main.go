// The reboot-required-detector daemon periodically checks if a reboot is required.
// It sets up node conditions that will eventually trigger node reboots.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/conditions"
	"github.com/kubereboot/kured/pkg/checkers"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	version = "unreleased"
)

var (
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
	var (
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
	flag.DurationVar(&period, "period", time.Minute, "period at which the main operations are done")
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

	if nodeID == "" {
		slog.Error(fmt.Sprintf("%s_NODE_ID environment variable or --node-id flag required", cli.EnvPrefix))
		os.Exit(1)
	}

	client := cli.KubernetesClientSetOrDie("", kubeconfig)

	slog.Info("Starting node-reboot-detector",
		"version", version,
		"node", nodeID,
		"rebootPeriod", period,
		"distribution", distribution,
		"metricsHost", metricsHost,
		"metricsPort", metricsPort,
	)

	// TODO: Add distribution-based defaulting for rebootSentinelCommand and rebootSentinelFile
	rebootChecker, err := checkers.NewRebootChecker(rebootSentinelCommand, rebootSentinelFile)
	if err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to build reboot checker: %v", err))
		os.Exit(4)
	}

	go maintainRebootRequiredCondition(period, rebootChecker, nodeID, ctx, client)

	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
		os.Exit(1)
	} // #nosec G114

}

func maintainRebootRequiredCondition(period time.Duration, rebootChecker checkers.Checker, nodeID string, ctx context.Context, client *kubernetes.Clientset) {
	for range time.Tick(period) {
		rebootRequired := rebootChecker.RebootRequired()
		slog.Debug("Reboot required check", "result", rebootRequired)
		if rebootRequired {
			rebootRequiredGauge.WithLabelValues(nodeID).Set(1)
		} else {
			rebootRequiredGauge.WithLabelValues(nodeID).Set(0)
		}
		nodeCondition := v1.NodeCondition{
			Type:               conditions.StringToConditionType(conditions.RebootRequiredConditionType),
			Status:             conditions.BoolToConditionStatus(rebootRequired),
			Reason:             conditions.RebootRequiredConditionReason,
			Message:            fmt.Sprintf("Kured sentinel check result is %t", rebootRequired),
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
		}
		if err := conditions.UpdateNodeCondition(ctx, client, nodeID, nodeCondition, period); err != nil {
			slog.Error("failed to update node condition", "error", err)
		}
	}
}
