package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/maintenances"
	"github.com/kubereboot/kured/pkg/conditions"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	cronlib "github.com/robfig/cron/v3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
)

var (
	version = "unreleased"
)

const (
	nodeMaintenanceServiceName = "node-maintenance-scheduler"
)

func init() {
	prometheus.MustRegister(maintenances.ActiveMaintenanceWindowGauge)
}

func main() {
	var (
		debug                           bool
		logFormat                       string
		kubeconfig, cmPrefix, namespace string
		metricsHost                     string
		metricsPort                     int
		concurrency                     int
		period                          time.Duration
	)
	flag.IntVar(&concurrency, "concurrency", 1, "Maximum number of nodes to be put under maintenance concurrently")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "maintenance configmap prefix")
	flag.DurationVar(&period, "period", time.Minute, "controller resync and maintenance assigner period")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.StringVar(&namespace, "namespace", "kube-system", "Namespace where maintenance configmaps live")
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
	windows, parsingError := maintenances.FetchWindows(ctx, client, namespace, cmPrefix)
	if parsingError != nil {
		slog.Error("failed to parse maintenance windows", "error", parsingError.Error())
		os.Exit(1)
	}

	mw := maintenances.NewWindows(windows)
	positiveConditions := []string{conditions.RebootRequiredConditionType}
	negativeConditions := []string{conditions.PreventRebootConditionType}
	maintenanceQueues := maintenances.NewQueues(concurrency)

	slog.Info("Starting node-maintenance-scheduler",
		"version", version,
		"debug", debug,
		"cmPrefix", cmPrefix,
		"concurrency", concurrency,
		"period", period,
		"knownMaintenanceWindows", len(windows),
	)

	slog.Info("You must reload this deployment in case of new or updated maintenance window(s)")

	// Maintenance manager setup
	c := cronlib.New(cronlib.WithLogger(cronLogger), cronlib.WithChain(cronlib.SkipIfStillRunning(cronLogger)))
	if err := loadAllCronJobs(c, mw, logger); err != nil {
		slog.Error("Failed to load maintenance windows into cron", "error", err.Error())
		os.Exit(1)
	}
	c.Start()

	// Controller handling node events
	kubeInformerFactory := informers.NewSharedInformerFactory(client, period)
	controller := NewController(logger, client,
		kubeInformerFactory.Core().V1().Nodes(),
		positiveConditions,
		negativeConditions,
		maintenanceQueues,
	)

	// The Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	kubeInformerFactory.Start(ctx.Done())

	go func() {
		if err := controller.Run(ctx, 2); err != nil {
			os.Exit(1)
		}
	}()

	// Now that:
	// - all the maintenance windows are set up
	// - the controller is running and watching node changes
	// We can start processing nodes if they belong to a maintenance window

	go startNodeMaintenance(ctx, period, client, mw, positiveConditions, negativeConditions, maintenanceQueues)

	// Closes on Ctrl-C
	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
		os.Exit(1)
	} // #nosec G114
}

// need to fix the leave node maintenance part.
func startNodeMaintenance(ctx context.Context, period time.Duration, client *kubernetes.Clientset, mw *maintenances.Windows, positiveConditions []string, negativeConditions []string, maintenanceQueues *maintenances.Queues) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case t := <-ticker.C:
			slog.Debug("new tick", "activeWindows", mw.String(), "activeSelectors", mw.ListSelectors(), "tick", t.Unix())
			allNodes, _ := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			for _, n := range allNodes.Items {
				slog.Debug("analysing node", "tick", t.Unix(), "node", n.ObjectMeta.Name)
				if !mw.ContainsNode(n) {
					slog.Debug("this node is not under any active maintenance window and is therefore ignored", "node", n.ObjectMeta.Name, "tick", t.Unix())
					continue
				}

				// Safety net to prevent a race condition where a node no longer matches the conditions between the informer change and this loop
				if !conditions.Matches(n.Status.Conditions, positiveConditions, negativeConditions) {
					// A node not matching the conditions anymore should be removed from all queues
					if removed := maintenanceQueues.Dequeue(n.ObjectMeta.Name); removed {
						slog.Info("Node removed from maintenance queues as it no longer matches conditions", "node", n.ObjectMeta.Name, "tick", t.Unix())
					}
					continue
				}

				// Node matches an active maintenance window and needs maintenance
				if active := maintenanceQueues.ProcessNode(n.ObjectMeta.Name); !active {
					slog.Debug("Node cannot be moved to active maintenance - concurrency limit reached or node not found in pending queue", "node", n.ObjectMeta.Name, "tick", t.Unix())
					continue
				}

				currentCondition := v1.NodeCondition{
					Type:               conditions.StringToConditionType(conditions.UnderMaintenanceConditionType),
					Status:             conditions.BoolToConditionStatus(true),
					Reason:             "Node under maintenance",
					Message:            fmt.Sprintf("%s is putting node under maintenance", nodeMaintenanceServiceName),
					LastHeartbeatTime:  metav1.Now(),
					LastTransitionTime: metav1.Now(),
				}

				if err := conditions.UpdateNodeCondition(ctx, client, n.ObjectMeta.Name, currentCondition); err != nil {
					slog.Error("Failed to set UnderMaintenance condition - needs human intervention as it will eventually block the queue", "node", n.ObjectMeta.Name, "tick", t.Unix(), "error", err.Error())
					continue
				}
				slog.Info("Node successfully moved to active maintenance", "node", n.ObjectMeta.Name, "tick", t.Unix())
			}

		case <-ctx.Done():
			slog.Info("Shutting down maintenance assigner")
			return
		}
	}
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
