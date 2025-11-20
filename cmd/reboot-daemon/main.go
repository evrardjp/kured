// The main controller for kured
// This package is a reference implementation on how to reboot your nodes based on the different
// tools present in this project's modules
package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/timewindow"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
	"k8s.io/client-go/informers"
)

var (
	version = "unreleased"

	// Command line flags (sorted alphabetically)
	alertFilter                     cli.RegexpValue
	alertFilterMatchOnly            bool
	alertFiringOnly                 bool
	annotateNodeProgress            bool
	concurrency                     int
	drainDelay                      time.Duration
	drainGracePeriod                int
	drainPodSelector                string
	drainTimeout                    time.Duration
	dsName                          string
	dsNamespace                     string
	lockAnnotation                  string
	lockReleaseDelay                time.Duration
	lockTTL                         time.Duration
	messageTemplateDrain            string
	messageTemplateReboot           string
	messageTemplateUncordon         string
	metricsHost                     string
	metricsPort                     int
	notifyURLs                      []string
	podSelectors                    []string
	postRebootNodeLabels            []string
	preRebootNodeLabels             []string
	preferNoScheduleTaintName       string
	prometheusURL                   string
	rebootDays                      []string
	rebootEnd                       string
	rebootSentinelCommand           string
	rebootSentinelFile              string
	rebootStart                     string
	skipWaitForDeleteTimeoutSeconds int
	timezone                        string
	forceReboot                     bool

	rebootBlockedCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "kured",
		Name:      "reboot_blocked_reason",
		Help:      "Reboot required was blocked by event.",
	}, []string{"node", "reason"})
)

const (
	// KuredNodeLockAnnotation is the canonical string value for the kured node-lock annotation
	KuredNodeLockAnnotation string = "kured.dev/kured-node-lock"
	// KuredRebootInProgressAnnotation is the canonical string value for the kured reboot-in-progress annotation
	KuredRebootInProgressAnnotation string = "kured.dev/kured-reboot-in-progress"
	// KuredMostRecentRebootNeededAnnotation is the canonical string value for the kured most-recent-reboot-needed annotation
	KuredMostRecentRebootNeededAnnotation string = "kured.dev/kured-most-recent-reboot-needed"
	// TODO: Replace this with runtime evaluation
	sigRTMinPlus5 = 34 + 5
)

func init() {
	prometheus.MustRegister(rebootBlockedCounter)
}

func main() {
	var (
		// Please continue sorting alphabetically :)
		debug         bool
		kubeconfig    string
		logFormat     string
		nodeID        string
		period        time.Duration
		rebootCommand string
		rebootDelay   time.Duration
		rebootMethod  string
		rebootSignal  int
	)
	// likewise, by flagname
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "text", "use text or json log format")
	flag.StringVar(&nodeID, "node-id", "", "node name on which this controller runs, should be passed down from spec.nodeName via KURED_NODE_ID environment variable")
	flag.DurationVar(&period, "period", time.Minute, "period is the controller resync period to ensure the node conditions are correctly read and a reboot is triggered")
	flag.StringVar(&rebootCommand, "reboot-command", "/bin/systemctl reboot", "command to run when a reboot is required")
	flag.DurationVar(&rebootDelay, "reboot-delay", 0, "delay reboot for this duration (default: 0, disabled)")
	flag.StringVar(&rebootMethod, "reboot-method", "command", "method to use for reboots. Available: command")
	flag.IntVar(&rebootSignal, "reboot-signal", sigRTMinPlus5, "signal to use for reboot, SIGRTMIN+5 by default.")

	flag.BoolVar(&alertFilterMatchOnly, "alert-filter-match-only", false, "Only block if the alert-filter-regexp matches active alerts")
	flag.BoolVar(&alertFiringOnly, "alert-firing-only", false, "only consider firing alerts when checking for active alerts")
	flag.BoolVar(&annotateNodeProgress, "annotate-nodes", false, "if set, the annotations 'kured.dev/kured-reboot-in-progress' and 'kured.dev/kured-most-recent-reboot-needed' will be given to nodes undergoing kured reboots")
	flag.BoolVar(&forceReboot, "force-reboot", false, "force a reboot even if the drain fails or times out")
	flag.DurationVar(&drainDelay, "drain-delay", 0, "delay drain for this duration (default: 0, disabled)")
	flag.DurationVar(&drainTimeout, "drain-timeout", 0, "timeout after which the drain is aborted (default: 0, infinite time)")
	flag.DurationVar(&lockReleaseDelay, "lock-release-delay", 0, "delay lock release for this duration (default: 0, disabled)")
	flag.DurationVar(&lockTTL, "lock-ttl", 0, "expire lock annotation after this duration (default: 0, disabled)")
	flag.IntVar(&concurrency, "concurrency", 1, "amount of nodes to concurrently reboot. Defaults to 1")
	flag.IntVar(&drainGracePeriod, "drain-grace-period", -1, "time in seconds given to each pod to terminate gracefully, if negative, the default value specified in the pod will be used")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.IntVar(&skipWaitForDeleteTimeoutSeconds, "skip-wait-for-delete-timeout", 0, "when seconds is greater than zero, skip waiting for the pods whose deletion timestamp is older than N seconds while draining a node")
	flag.StringArrayVar(&notifyURLs, "notify-url", nil, "notify URL for reboot notifications (can be repeated for multiple notifications)")
	flag.StringArrayVar(&podSelectors, "blocking-pod-selector", nil, "label selector identifying pods whose presence should prevent reboots")
	flag.StringSliceVar(&postRebootNodeLabels, "post-reboot-node-labels", nil, "labels to add to nodes after uncordoning")
	flag.StringSliceVar(&preRebootNodeLabels, "pre-reboot-node-labels", nil, "labels to add to nodes before cordoning")
	flag.StringSliceVar(&rebootDays, "reboot-days", timewindow.EveryDay, "schedule reboot on these days")
	flag.StringVar(&drainPodSelector, "drain-pod-selector", "", "only drain pods with labels matching the selector (default: '', all pods)")
	flag.StringVar(&dsName, "ds-name", "kured", "name of daemonset on which to place lock")
	flag.StringVar(&dsNamespace, "ds-namespace", "kube-system", "namespace containing daemonset on which to place lock")
	flag.StringVar(&lockAnnotation, "lock-annotation", KuredNodeLockAnnotation, "annotation in which to record locking node")
	flag.StringVar(&messageTemplateDrain, "message-template-drain", "Draining node %s", "message template used to notify about a node being drained")
	flag.StringVar(&messageTemplateReboot, "message-template-reboot", "Rebooting node %s", "message template used to notify about a node being rebooted")
	flag.StringVar(&messageTemplateUncordon, "message-template-uncordon", "Node %s rebooted & uncordoned successfully!", "message template used to notify about a node being successfully uncordoned")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.StringVar(&preferNoScheduleTaintName, "prefer-no-schedule-taint", "", "Taint name applied during pending node reboot (to prevent receiving additional pods from other rebooting nodes). Disabled by default. Set e.g. to \"kured.dev/kured-node-reboot\" to enable tainting.")
	flag.StringVar(&prometheusURL, "prometheus-url", "", "Prometheus instance to probe for active alerts")
	flag.StringVar(&rebootEnd, "end-time", "23:59:59", "schedule reboot only before this time of day")
	flag.StringVar(&rebootSentinelCommand, "reboot-sentinel-command", "", "command for which a zero return code will trigger a reboot command")
	flag.StringVar(&rebootSentinelFile, "reboot-sentinel", "/var/run/reboot-required", "path to file whose existence triggers the reboot command")
	flag.StringVar(&rebootStart, "start-time", "0:00", "schedule reboot only after this time of day")
	flag.StringVar(&timezone, "time-zone", "UTC", "use this timezone for schedule inputs")
	flag.Var(&alertFilter, "alert-filter-regexp", "alert names to ignore when checking for active alerts")

	flag.Parse()

	// Load flags from environment variables
	cli.LoadFromEnv()

	// set up signals so we handle the shutdown signal gracefully
	ctx := cli.SetupSignalHandler()

	logger := cli.NewLogger(debug, logFormat)
	// For all the old calls using logger
	slog.SetDefault(logger)

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

	//rebooter, err := reboot.NewRebooter(rebootMethod, rebootCommand, rebootSignal, rebootDelay, true, 1)
	//if err != nil {
	//	slog.Error(fmt.Sprintf("unrecoverable error - failed to construct system rebooter: %v", err))
	//	os.Exit(3)
	//}

	kubeInformerFactory := informers.NewSharedInformerFactoryWithOptions(client, period, informers.WithTweakListOptions(nodeNameFilter(nodeID)))

	controller := NewController(logger, client,
		kubeInformerFactory.Core().V1().Nodes(),
		period,
	)

	// The Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	kubeInformerFactory.Start(ctx.Done())

	go func() {
		if err := controller.Run(ctx, 1); err != nil {
			os.Exit(1)
		}
	}()

	//go rebootAsRequired(nodeID, rebooter, client, period)

	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil)) // #nosec G114
}

//func rebootAsRequired(nodeID string, rebooter reboot.Rebooter, client *kubernetes.Clientset, period time.Duration) {
//
//	preferNoScheduleTaint := k8soperations.NewTaint(client, nodeID, preferNoScheduleTaintName, v1.TaintEffectPreferNoSchedule)
//
//	// No reason to delay the first ticks.
//	// On top of it, we used to leak a goroutine, which was never garbage collected.
//	// Starting on go1.23, with Tick, we would have that goroutine garbage collected.
//	c := time.Tick(period)
//	for range c {
//		rebootRequired := checker.RebootRequired()
//		if !rebootRequired {
//			// Now cleaning up after a reboot
//
//			// Quickly allow rescheduling.
//			// The node could be still cordoned anyway
//			// Taint is based on whether or not the node is in the active maintenance window. If not for a reason or another, disable the taint immediately.
//			preferNoScheduleTaint.Disable()
//
//			// Test the API server first. If we cannot get node, we should not do anything.
//			node, err := client.CoreV1().Nodes().Get(context.TODO(), nodeID, metav1.GetOptions{})
//			if err != nil {
//				// Only debug level even though the API is dead: Kured should not worry about transient
//				// issues, the k8s cluster admin should be aware already
//				slog.Debug(fmt.Sprintf("error retrieving node object via k8s API: %v.\nPlease check API", err), "node", nodeID, "error", err)
//				continue
//			}
//
//			err = k8soperations.Uncordon(client, node, postRebootNodeLabels, messageTemplateUncordon)
//			if err != nil {
//				// Might be a transient API issue or a real problem. Inform the admin
//				slog.Info("unable to uncordon needs investigation", "node", nodeID, "error", err)
//				continue
//			}
//
//		} else {
//
//			// Act on reboot required.
//
//			// moved up because we should not put an annotation "Going to be rebooting", if
//			// we know well that this won't reboot. TBD as some ppl might have another opinion.
//
//			// Test the API server first. If we cannot get node, we should not do anything.
//			node, err := client.CoreV1().Nodes().Get(context.TODO(), nodeID, metav1.GetOptions{})
//			if err != nil {
//				// Not important enough to worry the admin
//				slog.Debug("error retrieving node object via k8s API, retrying at next tick", "node", nodeID, "error", err)
//				continue
//			}
//
//			// Prefer to not schedule pods onto this node to avoid draining the same pod multiple times.
//			preferNoScheduleTaint.Enable()
//
//			err = k8soperations.Drain(client, node, preRebootNodeLabels, drainTimeout, drainGracePeriod, skipWaitForDeleteTimeoutSeconds, drainPodSelector, drainDelay, messageTemplateDrain, notifier)
//
//			if err != nil {
//				if !forceReboot {
//					slog.Debug(fmt.Sprintf("Unable to cordon or drain %s: %v, will force-reboot by releasing lock and uncordon until next success", node.GetName(), err), "node", nodeID, "error", err)
//
//					// this is important -- if the next info not shown, it means that (in a normal or non-force reboot case)
//					// the drain was in error and the lock was NOT released.
//					// If shown, it is helping understand the "uncordoning".
//					// If the admin seems the node as cordoned even after trying a best-effort uncordon,
//					// the admin needs to take action (especially if the node was previously cordoned before the maintenance!)
//					slog.Info("Performing a best-effort uncordon after failed cordon and drain", "node", nodeID)
//					err := k8soperations.Uncordon(client, node, postRebootNodeLabels, messageTemplateUncordon)
//					if err != nil {
//						slog.Info("Failed to do best-effort uncordon", "node", nodeID, "error", err)
//					}
//					continue
//				}
//			}
//
//			// important lifecycle event
//			slog.Info(fmt.Sprintf("Triggering reboot for node %v", nodeID), "node", nodeID)
//
//			if err := rebooter.Reboot(); err != nil {
//				slog.Info("Error rebooting node", "node", nodeID, "error", err)
//				continue
//			}
//			for {
//				slog.Info("Waiting for reboot", "node", nodeID)
//				time.Sleep(time.Minute)
//			}
//		}
//	}
//}
