// The main controller for kured
// This package is a reference implementation on how to reboot your nodes based on the different
// tools present in this project's modules
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/reboot"
	"github.com/kubereboot/kured/internal/taints"
	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	kubectldrain "k8s.io/kubectl/pkg/drain"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	version = "unreleased"
	scheme  = runtime.NewScheme()
)

const (
	binaryName = "kured"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var (
		// Please continue sorting alphabetically :)
		debug                                bool
		drainDelay                           time.Duration
		drainGracePeriod                     int
		drainPodSelector                     string
		drainSkipWaitForDeleteTimeoutSeconds int
		drainTimeout                         time.Duration
		kubeconfig                           string
		logFormat                            string
		metricsHost                          string
		metricsPort                          int
		namespace                            string
		nodeID                               string
		period                               time.Duration
		preferNoScheduleTaintName            string
		probeBindAddress                     string
		rebootCommand                        string
		rebootDelay                          time.Duration
		rebootMethod                         string
		rebootSignal                         int
	)
	// likewise, by flag name
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.DurationVar(&drainDelay, "drain-delay", 0, "delay drain for this duration (default: 0, disabled)")
	flag.IntVar(&drainGracePeriod, "drain-grace-period", -1, "time in seconds given to each pod to terminate gracefully, if negative, the default value specified in the pod will be used")
	flag.StringVar(&drainPodSelector, "drain-pod-selector", "", "only drain pods with labels matching the selector (default: '', all pods)")
	flag.IntVar(&drainSkipWaitForDeleteTimeoutSeconds, "drain-skip-wait-for-delete-timeout", 0, "when seconds is greater than zero, skip waiting for the pods whose deletion timestamp is older than N seconds while draining a node")
	flag.DurationVar(&drainTimeout, "drain-timeout", 0, "timeout after which the drain is aborted (default: 0, infinite time)")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.StringVar(&namespace, "leader-election-namespace", "kube-system", "Namespace for leader election lock")
	flag.StringVar(&nodeID, "node-id", "", "node name on which this controller runs, should be passed down from spec.nodeName via KURED_NODE_ID environment variable")
	flag.DurationVar(&period, "period", time.Minute, "period is the controller resync period to ensure the node conditions are correctly read and a reboot is triggered")
	flag.StringVar(&preferNoScheduleTaintName, "prefer-no-schedule-taint", "", "Taint name applied during pending node reboot (to prevent receiving additional pods from other rebooting nodes). Disabled by default. Set e.g. to \"kured.dev/kured-node-reboot\" to enable tainting.")
	flag.StringVar(&probeBindAddress, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&rebootCommand, "reboot-command", "/bin/systemctl reboot", "command to run when a reboot is required")
	flag.DurationVar(&rebootDelay, "reboot-delay", 0, "delay reboot for this duration (default: 0, disabled)")
	flag.StringVar(&rebootMethod, "reboot-method", "signal", "method to use for reboots. Available: command, signal")
	flag.IntVar(&rebootSignal, "reboot-signal", reboot.SigRTMinPlus5, "signal to use for reboot, SIGRTMIN+5 by default.")

	flag.Parse()

	// Load flags from environment variables
	cli.LoadFromEnv()

	// set up signals so we handle the shutdown signal gracefully
	ctx := cli.SetupSignalHandler()

	logger := cli.NewLogger(debug, logFormat)
	slog.SetDefault(logger)                                // for generic log.logger
	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler())) // for controller-runtime's logger
	setupLog := ctrl.Log.WithName(binaryName)              // for controller-runtime logger
	klog.SetLogger(logr.FromSlogHandler(logger.Handler())) // for client-go's tool loggers (appears in leader election)

	client := cli.KubernetesClientSetOrDie("", kubeconfig)

	// Core initialisation, error 1 on failure.
	if nodeID == "" {
		slog.Error("KURED_NODE_ID environment variable required")
		os.Exit(1)
	}

	slog.Info("Starting Kubernetes Reboot Daemon",
		"version", version,
		"period", period,
		"metricsHost", metricsHost,
		"metricsPort", metricsPort,
		"debug", debug,

		"node", nodeID,
		"method", rebootMethod,
		"taint", fmt.Sprintf("preferNoSchedule taint: (%s)", preferNoScheduleTaintName),
	)

	metricsBindAddress := metricsHost + ":" + strconv.Itoa(metricsPort)

	rebooterConfig := &reboot.Config{
		Method:     rebootMethod,
		Command:    rebootCommand,
		Signal:     rebootSignal,
		Delay:      rebootDelay,
		Privileged: true,
		PID:        1,
	}

	rebooter, errRebooter := rebooterConfig.NewRebooter()
	if errRebooter != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to construct system rebooter: %v", errRebooter))
		os.Exit(3)
	}

	drainHelper := &kubectldrain.Helper{
		Client:                          client,
		Ctx:                             ctx,
		Force:                           true,
		GracePeriodSeconds:              drainGracePeriod,
		IgnoreAllDaemonSets:             true,
		Timeout:                         drainTimeout,
		DeleteEmptyDirData:              true,
		PodSelector:                     drainPodSelector,
		SkipWaitForDeleteTimeoutSeconds: drainSkipWaitForDeleteTimeoutSeconds,
		Out:                             os.Stdout,
		ErrOut:                          os.Stderr,
	}

	preferNoScheduleTaint := taints.New(ctx, client, nodeID, preferNoScheduleTaintName, corev1.TaintEffectPreferNoSchedule)

	mgr, errNewManager := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsBindAddress,
			SecureServing: false,
		},
		HealthProbeBindAddress: probeBindAddress,
		LeaderElection:         false,
	})
	if errNewManager != nil {
		setupLog.Error(errNewManager, "unable to start manager")
		os.Exit(1)
	}

	setupLog.Info("Setting up kured controller")
	c := reboot.NewController(logger, mgr, nodeID, rebooter, period, drainDelay, preferNoScheduleTaint, drainHelper)
	if err := c.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "kured")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	// Useless ready check for now
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
