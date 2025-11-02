package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/maintenances"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

var (
	version = "unreleased"
)

func init() {
	prometheus.MustRegister(maintenances.UnderMaintenanceGauge)
}

//type nodeOps interface {
//	ListNodes(ctx context.Context, selector string) ([]corev1.Node, error)
//	GetNode(ctx context.Context, nodeName string) (*corev1.Node, error)
//	SetNodeCondition(ctx context.Context, nodeName string, condType string, status corev1.ConditionStatus, reason string) error
//}
//
//type realNodeOps struct {
//	client *kubernetes.Clientset
//}
//
//func (r *realNodeOps) ListNodes(ctx context.Context, selector string) ([]corev1.Node, error) {
//	lo := metav1.ListOptions{}
//	if selector != "" {
//		lo.LabelSelector = selector
//	}
//	list, err := r.client.CoreV1().Nodes().List(ctx, lo)
//	if err != nil {
//		return nil, err
//	}
//	return list.Items, nil
//}
//
//func (r *realNodeOps) GetNode(ctx context.Context, nodeName string) (*corev1.Node, error) {
//	return r.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
//}
//
//func (r *realNodeOps) SetNodeCondition(ctx context.Context, nodeName string, condType string, status corev1.ConditionStatus, reason string) error {
//	now := metav1.NewTime(time.Now())
//	cond := corev1.NodeCondition{
//		Type:               corev1.NodeConditionType(condType),
//		Status:             status,
//		LastTransitionTime: now,
//		Reason:             reason,
//	}
//	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
//		latest, err := r.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
//		if err != nil {
//			return err
//		}
//		found := false
//		conds := latest.Status.Conditions
//		for i := range conds {
//			if string(conds[i].Type) == condType {
//				conds[i] = cond
//				found = true
//				break
//			}
//		}
//		if !found {
//			conds = append(conds, cond)
//		}
//		latest.Status.Conditions = conds
//		_, err = r.client.CoreV1().Nodes().UpdateStatus(ctx, latest, metav1.UpdateOptions{})
//		return err
//	})
//}

func handleNodeDeletion(obj interface{}, globalQueue *maintenances.Queue) {
	globalQueue.Dequeue(obj.(*corev1.Node).Name)
}

func handleNodeUpdate(obj interface{}, globalQueue *maintenances.Queue) {
	node := obj.(*corev1.Node).DeepCopy()
	if node.DeletionTimestamp != nil {
		handleNodeDeletion(obj, globalQueue)
	}
	// If we don't know, we should not queue the node
	if !hasCondition(node, "NeedsReboot", corev1.ConditionTrue) {
		globalQueue.Dequeue(node.Name)
	} else {
		// explicitly in need of reboot, queue this
		globalQueue.Enqueue(node)
		// Only apply prometheus change if the node is in maintenance window!
	}
}

func main() {
	var (
		debug                                                   bool
		kubeconfig, cmPrefix, namespace, metricsHost, logFormat string
		maxRebootConcurrency, metricsPort                       int
		period                                                  time.Duration
	)
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "maintenance configmap prefix")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.IntVar(&maxRebootConcurrency, "max-reboot-concurrency", 10, "maximum amount of nodes in maintenance at any point in time")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.StringVar(&namespace, "namespace", "kube-system", "Namespace where maintenance configmaps live")
	flag.DurationVar(&period, "period", time.Minute, "period at which the main operations are done")

	flag.Parse()

	// Load flags from environment variables. Remember the prefix KURED_!
	cli.LoadFromEnv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	// For all the old calls using logger
	slog.SetDefault(logger)

	client := cli.KubernetesClientSetOrDie(kubeconfig)
	windows := maintenances.LoadWindowsOrDie(ctx, client, namespace, cmPrefix)

	slog.Info("Starting node-maintenance-scheduler",
		"version", version,
		"debug", debug,
		"cmPrefix", cmPrefix,
		"rebootConcurrency", maxRebootConcurrency,
		"metricsHost", metricsHost,
		"metricsPort", metricsPort,
		"knownMaintenanceWindows", len(windows),
		"period", period,
	)
	slog.Info("You must reload this deployment in case of new or updated maintenance window(s)")

	globalQueue := maintenances.NewQueue(maxRebootConcurrency)

	// This is very lightweight.
	// It might need to move to a sample-controller approach of having workqueues for large clusters.
	// It needs to be tested first, as it is very simple.
	factory := informers.NewSharedInformerFactory(client, 0)
	informer := factory.Core().V1().Nodes().Informer()
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handleNodeUpdate(obj, globalQueue)
		},
		UpdateFunc: func(old, new interface{}) {
			handleNodeUpdate(new, globalQueue)
		},
		DeleteFunc: func(obj interface{}) {
			handleNodeDeletion(obj, globalQueue)
		},
	})

	go informer.RunWithContext(ctx)

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Wait for informer caches to sync
	if !cache.WaitForCacheSync(stopCh, informer.HasSynced) {
		slog.Error("failed waiting for lease informer sync")
		os.Exit(3)
	}

	go PutNodeInMaintainance(ctx, period, windows, globalQueue)

	select {}
}

// PutNodeInMaintainance runs the maintenance loop.
// It finds all the active window's nodes (matching their respective selectors), putting them in a new map (currentTickMap).
// It compare the currentTickMap with the complete list of nodes, so that all the nodes absent of the currentTickMap are marked as "outside a maintenance" during this tick (prom under_maintenance=0).
// TODO: Check if we can rely on the informer, or if that's feeling weird. If it's too slow with the informer, then make an explicit code path for that.
// The nodes present in the currentTickMap are all in the window, but we do not know yet if they need Reboot or not.
// For that, we have two sources of information: The global Pending queue and the node information.
// We could use the lastTransition state to sort all those, but it's more tedious than checking the globalQueue's pending list.
// So, we iterate over the globalQueue's pending list.
// If the node is not in the currentTickMap, it needs reboot but cannot be under maintenance right now. Set the prom under_maintenance=0
// If the node is in the currentTickMap, we double-check whether:
//   - We can process the queue (max capacity)
//     If we cannot, we break/skip the whole tick: Tick is full, so we need some node to finish its reboot first (and get dequeued).
//   - The node is up to date in global Queue:
//     Compare the node information in the currentTickMap with the global Queue -- by checking if the node is still in need of reboot (to avoid a race condition, for example, the node is just out of maintenance, but the informer didn't get the memo yet.)
//     If not, dequeue it and continue for other nodes in the tick.
//     If yes, we put the node in the active queue for maintenance.
//     If successful, process the node by doing the api calls to patch the nodes.
func PutNodeInMaintainance(ctx context.Context, period time.Duration, windows []*maintenances.Window, globalQueue *maintenances.Queue) {
	ticker := time.NewTicker(period)
	for now := range ticker.C {
		_ = now // keep for potential time injection/tests
		currentTickMap := map[string]*corev1.Node{}
		for mwindowIdx := range windows {
			mwindow := windows[mwindowIdx]
			if !mwindow.IsActive(now) {
				continue
			}

	}

}

//ops := &realNodeOps{client: client}

//go WatchNodesConditions(ctx, ops, gs, &mu)
//
//ticker := time.NewTicker(10 * time.Second)
//defer ticker.Stop()
//for now := range ticker.C {
//	_ = now // keep for potential time injection/tests
//	mu.Lock()
//	if err := enqueueCandidates(ctx, ops, windows, gs); err != nil {
//		mu.Unlock()
//		fmt.Fprintf(os.Stderr, "enqueue candidates failed: %v\n", err)
//		continue
//	}
//	processQueue(ctx, ops, gs, maxRebootConcurrency)
//	mu.Unlock()
//}

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
