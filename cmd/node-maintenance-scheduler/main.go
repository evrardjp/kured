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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	cronlib "github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/klog/v2"
)

var (
	version         = "unreleased"
	activeSelectors map[string]labels.Selector // global map of active maintenance window selectors
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
		period                          time.Duration
	)
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "maintenance configmap prefix")
	flag.DurationVar(&period, "period", time.Minute, "controller resync period")
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

	logger := NewLogger(debug, logFormat)
	// For all the old calls using logger
	slog.SetDefault(logger)
	cronLogger := &cronSlogAdapter{logger}

	//var windows []*maintenances.Window
	//windows = append(windows, &maintenances.Window{
	//	Name:         "maintenance-a",
	//	Schedule:     "@every 2s",
	//	Duration:     10 * time.Second,
	//	NodeSelector: labels.Everything(),
	//})
	//windows = append(windows, &maintenances.Window{
	//	Name:         "maintenance-b",
	//	Schedule:     "@every 2s",
	//	Duration:     1 * time.Second,
	//	NodeSelector: labels.Everything(),
	//})

	client := cli.KubernetesClientSetOrDie("", kubeconfig)
	windows := maintenances.LoadWindowsOrDie(ctx, client, namespace, cmPrefix)

	slog.Info("Starting node-maintenance-scheduler",
		"version", version,
		"debug", debug,
		"cmPrefix", cmPrefix,
		"knownMaintenanceWindows", len(windows),
	)

	// Maintenance manager setup
	c := cronlib.New(cronlib.WithLogger(cronLogger), cronlib.WithChain(cronlib.SkipIfStillRunning(cronLogger)))
	loadAllCronJobs(ctx, c, windows, logger)
	c.Start()

	// Controller handling node events
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(client, period)
	controller := NewController(ctx, kubeClient, exampleClient,
		kubeInformerFactory.Apps().V1().Deployments(),
		exampleInformerFactory.Samplecontroller().V1alpha1().Foos())

	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(ctx.done())
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	kubeInformerFactory.Start(ctx.Done())

	if err = controller.Run(ctx, 2); err != nil {
		logger.Error(err, "Error running controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	//nodes, _ := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	//for _, node := range nodes.Items {
	//	for _, w := range windows {
	//		if w.IsActive(time.Now()) && w.ContainsNode(node.Labels) {
	//			// node matches an active maintenance window
	//			slog.Info("Node %s is under maintenance window %s\n", node.Name, w.Name)
	//		}
	//	}
	//}
	// Closes on Ctrl-C
	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
		slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
		os.Exit(1)
	} // #nosec G114
	select {}

}

func NewLogger(debug bool, logFormat string) *slog.Logger {
	var logger *slog.Logger
	handlerOpts := &slog.HandlerOptions{}
	if debug {
		handlerOpts.Level = slog.LevelDebug
	}
	switch logFormat {
	case "json":
		logger = slog.New(slog.NewJSONHandler(os.Stdout, handlerOpts))
	case "text":
		logger = slog.New(slog.NewTextHandler(os.Stdout, handlerOpts))
	default:
		logger = slog.New(slog.NewJSONHandler(os.Stdout, handlerOpts))
		logger.Info("incorrect configuration for logFormat, using json handler")
	}
	return logger
}

type cronSlogAdapter struct {
	*slog.Logger
}

func (a *cronSlogAdapter) Info(msg string, keysAndValues ...interface{}) {
	a.Logger.Info(msg, keysAndValues...)
}

func (a *cronSlogAdapter) Error(err error, msg string, keysAndValues ...interface{}) {
	// Prepend a key/value pair for the error.
	// You can name the key whatever you want; "error" is conventional.
	a.Logger.Error(msg, append([]any{"error", err.Error()}, keysAndValues...)...)
}

func loadAllCronJobs(ctx context.Context, c *cronlib.Cron, windows []*maintenances.Window, logger *slog.Logger) {
	logger.Info("You must reload this deployment in case of new or updated maintenance window(s)")
	for _, window := range windows {
		window := window
		_, err := c.AddFunc(window.Schedule, startWindow(window, logger))
		if err != nil {
			slog.Error("Problem adding function to schedule", "error", err.Error())
		}
	}
}

func startWindow(w *maintenances.Window, logger *slog.Logger) func() {
	return func() {
		logger.Info("Starting maintenance window", "window", w.Name)
		maintenances.ActiveMaintenanceWindowGauge.WithLabelValues(w.Name).Set(1)
		activeSelectors[w.Name] = w.NodeSelector

		time.Sleep(w.Duration)
		logger.Info("End of maintenance window", "window", w.Name)
		maintenances.ActiveMaintenanceWindowGauge.WithLabelValues(w.Name).Set(0)
		delete(activeSelectors, w.Name)
	}
}

//
//func enqueueCandidates(ctx context.Context, ops nodeOps, windows []*maintenances.Window, gs *globalState) error {
//	seen := map[string]struct{}{} // avoid duplicates
//	for n := range gs.active {
//		seen[n] = struct{}{}
//	}
//	for _, p := range gs.pending {
//		seen[p] = struct{}{}
//	}
//
//	now := time.Now()
//	for _, w := range windows {
//		if !windowActive(w, now) {
//			continue
//		}
//		nodes, err := ops.ListNodes(ctx, w.nodeSelector)
//		if err != nil {
//			// If any error happened during any node listing,
//			// we can't be sure the list is complete or accurate.
//			// We should not continue to browse all windows and retry later to save the pain to the API server.
//			return fmt.Errorf("error list nodes while browsing maintenance window %s, %w", w.name, err)
//		}
//		for _, n := range nodes {
//			if !hasCondition(&n, "NeedsReboot", corev1.ConditionTrue) {
//				continue
//			}
//			if _, ok := seen[n.Name]; ok {
//				continue
//			}
//			gs.pending = append(gs.pending, n.Name)
//			seen[n.Name] = struct{}{}
//		}
//	}
//	return nil
//}
//
//func processQueue(ctx context.Context, ops nodeOps, gs *globalState, maxRebootConcurrency int) {
//	// process pending queue up to concurrency limit
//	for len(gs.active) < maxRebootConcurrency && len(gs.pending) > 0 {
//		nodeName := gs.pending[0]
//		gs.pending = gs.pending[1:]
//
//		// verify node still needs maintenance
//		node, err := ops.GetNode(ctx, nodeName)
//		if err != nil {
//			if k8serrors.IsNotFound(err) {
//				// Node was deleted, skip it
//				fmt.Printf("skipping deleted node %s\n", nodeName)
//				continue
//			}
//			fmt.Fprintf(os.Stderr, "get node %s: %v\n", nodeName, err)
//			continue
//
//		}
//		if !hasCondition(node, "NeedsReboot", corev1.ConditionTrue) {
//			continue
//		}
//
//		if err := ops.SetNodeCondition(ctx, nodeName, "kured.dev/UnderMaintenance", corev1.ConditionTrue, "Node moved to active queue"); err != nil {
//			fmt.Fprintf(os.Stderr, "set undermaintenance %s: %v\n", nodeName, err)
//			continue
//		}
//
//		gs.active[nodeName] = struct{}{}
//		fmt.Printf("node %s -> maintenance started\n", nodeName)
//	}
//}
//
//// WatchNodesConditions manages the queues of nodes based on the NeedsReboot condition.
//// If NeedsReboot is removed, the node is removed from any queue.
//// This allows external actors to remove nodes from maintenance
//// e.g., if a node is manually rebooted or patched.
//// This runs in a separate goroutine to avoid blocking the main loop.
//func WatchNodesConditions(ctx context.Context, ops *realNodeOps, gs *globalState, mu *sync.Mutex) {
//	watcher, err := ops.client.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{})
//	if err != nil {
//		fmt.Fprintf(os.Stderr, "node watch: %v\n", err)
//		return
//	}
//	defer watcher.Stop()
//	for ev := range watcher.ResultChan() {
//		if ev.Type == watch.Error {
//			continue
//		}
//		n, ok := ev.Object.(*corev1.Node)
//		if !ok {
//			continue
//		}
//		mu.Lock()
//		if !hasCondition(n, "NeedsReboot", corev1.ConditionTrue) {
//			if _, present := gs.active[n.Name]; present {
//				_ = ops.SetNodeCondition(ctx, n.Name, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "NeedsReboot condition removed")
//				delete(gs.active, n.Name)
//				fmt.Printf("node %s -> maintenance complete\n", n.Name)
//			} else {
//				for i, nodeName := range gs.pending {
//					if nodeName == n.Name {
//						gs.pending = append(gs.pending[:i], gs.pending[i+1:]...)
//						fmt.Printf("node %s -> removed from pending queue\n", n.Name)
//						break
//					}
//				}
//			}
//		}
//		mu.Unlock()
//	}
//}

func hasCondition(node *corev1.Node, condType string, status corev1.ConditionStatus) bool {
	for _, c := range node.Status.Conditions {
		if string(c.Type) == condType && c.Status == status {
			return true
		}
	}
	return false
}
