package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// Explanation of assumptions (kept short):
// - ConfigMaps are stored in a single namespace (default: "kured") and their names start with a prefix (default: "kured-maintenance-")
// - Each ConfigMap's Data contains keys: "reason", "startTime" (cron expression), "duration" (minutes), "nodeSelector" (label selector, e.g. "node-role.kubernetes.io/worker=true"), "concurrency" (int)
// - startTime is a cron expression. We use robfig/cron v3 to parse and schedule.
// - Condition types used: "NeedsReboot" and custom "kured.dev/UnderMaintenance".

var (
	metrics = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "kured_node_maintenance_status", Help: "1 if maintenance in progress for the node and window, 0 otherwise"},
		[]string{"maintenance_window", "node"},
	)
)

func main() {
	// Flags
	var kubeconfig string
	var cmPrefix string
	var metricsAddr string
	var cmNamespace string
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional path to kubeconfig (use in-cluster if empty)")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "prefix for ConfigMap names that define maintenance windows")
	flag.StringVar(&metricsAddr, "metrics-addr", ":9090", "address to serve Prometheus metrics on")
	flag.StringVar(&cmNamespace, "namespace", "kube-system", "namespace where maintenance ConfigMaps live")
	flag.Parse()

	prometheus.MustRegister(metrics)
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		_ = http.ListenAndServe(metricsAddr, nil)
	}()

	// Kubernetes client
	config, err := loadKubeConfig(kubeconfig)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load kube config: %v\n", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create clientset: %v\n", err)
		os.Exit(1)
	}

	// Discover maintenance ConfigMaps and schedule handlers using cron
	cms, err := clientset.CoreV1().ConfigMaps(cmNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to list configmaps: %v\n", err)
		os.Exit(1)
	}

	c := cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)))

	for _, cm := range cms.Items {
		if !strings.HasPrefix(cm.Name, cmPrefix) {
			continue
		}
		mw, err := parseConfigMap(cm)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "invalid maintenance config %s: %v\n", cm.Name, err)
			continue
		}

		// capture local copies for closure
		localMW := mw
		localClient := clientset
		_, err = c.AddFunc(localMW.cronExpr, func() { runMaintenanceWindow(context.Background(), localClient, localMW) })
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to schedule maintenance %s: %v\n", localMW.name, err)
			continue
		}
		fmt.Printf("scheduled maintenance %s with cron '%s' duration %s\n", localMW.name, localMW.cronExpr, localMW.duration)
	}

	c.Start()
	// block forever
	select {}
}

// loadKubeConfig loads in-cluster config or falls back to kubeconfig path.
func loadKubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

// maintenanceWindow struct represents the parsed data from the ConfigMap.
type maintenanceWindow struct {
	name         string
	reason       string
	cronExpr     string
	duration     time.Duration
	nodeSelector string
	concurrency  int
}

func parseConfigMap(cm corev1.ConfigMap) (*maintenanceWindow, error) {
	d := cm.Data
	name := cm.Name
	reason := d["reason"]
	cronExpr := strings.TrimSpace(d["startTime"])
	if cronExpr == "" {
		return nil, fmt.Errorf("missing startTime (cron expression) in %s", name)
	}
	durStr := d["duration"]
	durMin, err := strconv.Atoi(durStr)
	if err != nil {
		return nil, fmt.Errorf("invalid duration in %s: %v", name, err)
	}
	conStr := d["concurrency"]
	con := 1
	if conStr != "" {
		con, err = strconv.Atoi(conStr)
		if err != nil {
			return nil, fmt.Errorf("invalid concurrency in %s: %v", name, err)
		}
	}
	sel := d["nodeSelector"]
	if sel == "" {
		sel = ""
	}
	// maybe add a "command to run"
	return &maintenanceWindow{
		name:         name,
		reason:       reason,
		cronExpr:     cronExpr,
		duration:     time.Duration(durMin) * time.Minute,
		nodeSelector: sel,
		concurrency:  con,
	}, nil
}

// runMaintenanceWindow executes the window: continuously discover nodes during the window, build queue, watch nodes and manage conditions.
func runMaintenanceWindow(ctx context.Context, clientset *kubernetes.Clientset, mw *maintenanceWindow) {
	ctx, cancel := context.WithTimeout(ctx, mw.duration)
	defer cancel()

	fmt.Printf("running maintenance %s for %s\n", mw.name, mw.duration)

	// queue management
	var mu sync.Mutex
	pending := make([]corev1.Node, 0)
	pendingSet := map[string]bool{}
	inProgress := map[string]corev1.Node{}
	sem := make(chan struct{}, mw.concurrency)

	// watch nodes for changes (to detect when NeedsReboot becomes false)
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	go watchNodes(watchCtx, clientset, mw, &mu, inProgress, sem)

	// ticker periodically lists nodes and enqueues those that require reboot
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	enqueue := func(n corev1.Node) {
		if _, ok := inProgress[n.Name]; ok {
			return
		}
		if pendingSet[n.Name] {
			return
		}
		pending = append(pending, n)
		pendingSet[n.Name] = true
	}

	// initial enqueue
	func() {
		mu.Lock()
		defer mu.Unlock()
		listOptions := metav1.ListOptions{}
		if mw.nodeSelector != "" {
			listOptions.LabelSelector = mw.nodeSelector
		}
		nodes, err := clientset.CoreV1().Nodes().List(ctx, listOptions)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to list nodes at window start: %v\n", err)
			return
		}
		for _, n := range nodes.Items {
			if hasCondition(&n, "NeedsReboot", corev1.ConditionTrue) {
				enqueue(n)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			// window ended
			mu.Lock()
			for _, n := range inProgress {
				_ = setNodeCondition(context.Background(), clientset, &n, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "window-ended")
				metrics.WithLabelValues(mw.name, n.Name).Set(0)
			}
			mu.Unlock()
			fmt.Printf("maintenance %s finished\n", mw.name)
			return
		case <-ticker.C:
			// discover new nodes during window
			listOptions := metav1.ListOptions{}
			if mw.nodeSelector != "" {
				listOptions.LabelSelector = mw.nodeSelector
			}
			nodes, err := clientset.CoreV1().Nodes().List(ctx, listOptions)
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "failed to list nodes during window: %v\n", err)
				continue
			}
			mu.Lock()
			for _, n := range nodes.Items {
				if hasCondition(&n, "NeedsReboot", corev1.ConditionTrue) {
					enqueue(n)
				}
			}
			// try to fill concurrency slots
			for len(inProgress) < mw.concurrency && len(pending) > 0 {
				n := pending[0]
				pending = pending[1:]
				delete(pendingSet, n.Name)
				// mark under maintenance
				if err := setNodeCondition(context.Background(), clientset, &n, "kured.dev/UnderMaintenance", corev1.ConditionTrue, mw.reason); err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "failed to set under maintenance on %s: %v\n", n.Name, err)
					continue
				}
				inProgress[n.Name] = n
				metrics.WithLabelValues(mw.name, n.Name).Set(1)
				sem <- struct{}{}
				fmt.Printf("node %s entered maintenance queue for %s\n", n.Name, mw.name)
			}
			mu.Unlock()
		}
	}
}

// watchNodes watches node updates and when NeedsReboot becomes False for a node in inProgress, it will remove it and free a slot.
func watchNodes(ctx context.Context, clientset *kubernetes.Clientset, mw *maintenanceWindow, mu *sync.Mutex, inProgress map[string]corev1.Node, sem chan struct{}) {
	watcher, err := clientset.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to start node watch: %v\n", err)
		return
	}
	defer watcher.Stop()

	for ev := range watcher.ResultChan() {
		if ev.Type == watch.Error {
			continue
		}
		node, ok := ev.Object.(*corev1.Node)
		if !ok {
			continue
		}
		mu.Lock()
		if _, present := inProgress[node.Name]; present {
			if !hasCondition(node, "NeedsReboot", corev1.ConditionTrue) {
				// remove from inProgress
				delete(inProgress, node.Name)
				// unset under maintenance condition
				_ = setNodeCondition(context.Background(), clientset, node, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "reboot-complete")
				metrics.WithLabelValues(mw.name, node.Name).Set(0)
				// free a slot
				select {
				case <-sem:
				default:
				}
				fmt.Printf("node %s left maintenance (reboot-required=false)\n", node.Name)
			}
		}
		mu.Unlock()
	}
}

// hasCondition checks if the node has a condition type with the specified status.
func hasCondition(node *corev1.Node, condType string, status corev1.ConditionStatus) bool {
	for _, c := range node.Status.Conditions {
		if string(c.Type) == condType && c.Status == status {
			return true
		}
	}
	return false
}

// setNodeCondition updates the node status condition type with the provided status.
func setNodeCondition(ctx context.Context, clientset *kubernetes.Clientset, node *corev1.Node, condType string, status corev1.ConditionStatus, reason string) error {
	// we will patch the status.conditions array using JSON patch to be safe for concurrent updates.
	now := metav1.NewTime(time.Now())
	cond := corev1.NodeCondition{
		Type:               corev1.NodeConditionType(condType),
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            "",
	}

	// Fetch latest node to avoid conflicts and build a strategic merge patch on status.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := clientset.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// update or append condition
		existing := false
		conds := latest.Status.Conditions
		for i := range conds {
			if string(conds[i].Type) == condType {
				conds[i] = cond
				existing = true
				break
			}
		}
		if !existing {
			conds = append(conds, cond)
		}
		latest.Status.Conditions = conds
		_, err = clientset.CoreV1().Nodes().UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}
