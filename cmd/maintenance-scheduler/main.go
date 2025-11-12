package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/maintenances"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	cronlib "github.com/robfig/cron/v3" // same as kubernetes
	"k8s.io/client-go/informers"
)

var (
	version = "unreleased"
)

const (
	nodeMaintenanceServiceName = "maintenance-scheduler"
)

func init() {
	prometheus.MustRegister(maintenances.ActiveMaintenanceWindowGauge)
}

func main() {
	var (
		cmNamespace string
		cmPrefix    string
		debug       bool
		kubeconfig  string
		logFormat   string
		metricsHost string
		metricsPort int
		period      time.Duration
	)
	flag.StringVar(&cmNamespace, "cm-namespace", "kube-system", "Namespace where maintenance configmaps live")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "maintenance configmap prefix")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.DurationVar(&period, "period", time.Minute, "controller resync and maintenance assigner period")

	flag.Parse()

	// Load flags from environment variables. Remember the prefix KURED_!
	cli.LoadFromEnv()

	// set up signals so we handle the shutdown signal gracefully
	ctx := cli.SetupSignalHandler()

	logger := cli.NewLogger(debug, logFormat)
	// For all the old calls using logger
	slog.SetDefault(logger)
	cronLogger := &cli.CronSlogAdapter{Logger: logger}

	client := cli.KubernetesClientSetOrDie("", kubeconfig)
	windows, parsingError := maintenances.FetchWindows(ctx, client, cmNamespace, cmPrefix)
	if parsingError != nil {
		slog.Error("failed to parse maintenance windows", "error", parsingError.Error())
		os.Exit(1)
	}

	slog.Info("Starting maintenance-scheduler",
		"version", version,
		"debug", debug,
		"cmPrefix", cmPrefix,
		"period", period,
		"knownMaintenanceWindows", len(windows),
	)

	// Todo, update the controller to dynamically reload windows without restart by watching cms
	// delete of the cm would delete the attached cronjobs
	// modification would delete and add the cronjobs
	// addition would add the cronjobs
	slog.Info("You must reload this deployment in case of new or updated maintenance window(s)")

	mw := maintenances.NewWindows(windows)

	// Maintenance manager setup
	c := cronlib.New(cronlib.WithLogger(cronLogger), cronlib.WithChain(cronlib.SkipIfStillRunning(cronLogger)))
	if err := loadAllCronJobs(c, mw, logger); err != nil {
		slog.Error("Failed to load maintenance windows into cron", "error", err.Error())
		os.Exit(1)
	}
	c.Start()

	// Controller handling node events to guarantee the placement of nodes into maintenance if they belong to an active window
	kubeInformerFactory := informers.NewSharedInformerFactory(client, period)
	controller := NewController(logger, client,
		kubeInformerFactory.Core().V1().Nodes(),
		mw,
	)

	// The Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	kubeInformerFactory.Start(ctx.Done())

	go func() {
		if err := controller.Run(ctx, 2); err != nil {
			os.Exit(1)
		}
	}()

	// Closes on Ctrl-C
	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
		os.Exit(1)
	} // #nosec G114
}

func loadAllCronJobs(c *cronlib.Cron, mw *maintenances.Windows, logger *slog.Logger) error {
	for _, window := range mw.AllWindows {
		logger.Debug("Loading maintenance window into cron", "window", window.Name, "schedule", window.Schedule)
		if _, err := c.AddFunc(window.Schedule, startWindow(window.Name, mw, logger)); err != nil {
			return fmt.Errorf("problem adding function to schedule %s: %w", window.Name, err)
		}
	}
	return nil
}

func startWindow(windowName string, mw *maintenances.Windows, logger *slog.Logger) func() {
	return func() {
		logger.Info("Starting maintenance window", "window", windowName)
		mw.Start(windowName)
		time.Sleep(mw.AllWindows[windowName].Duration)
		logger.Info("End of maintenance window", "window", windowName)
		mw.End(windowName)
	}
}
