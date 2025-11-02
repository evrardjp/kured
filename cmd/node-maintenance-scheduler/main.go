package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	cronlib "github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

type maintenanceWindow struct {
	name         string
	cronExpr     string
	duration     time.Duration
	nodeSelector string
	schedule     cronlib.Schedule
}

type globalState struct {
	pending []string            // nodes waiting to start maintenance
	active  map[string]struct{} // nodes currently in maintenance
}

type nodeOps interface {
	ListNodes(ctx context.Context, selector string) ([]corev1.Node, error)
	GetNode(ctx context.Context, nodeName string) (*corev1.Node, error)
	SetNodeCondition(ctx context.Context, nodeName string, condType string, status corev1.ConditionStatus, reason string) error
}

type realNodeOps struct {
	client *kubernetes.Clientset
}

func (r *realNodeOps) ListNodes(ctx context.Context, selector string) ([]corev1.Node, error) {
	lo := metav1.ListOptions{}
	if selector != "" {
		lo.LabelSelector = selector
	}
	list, err := r.client.CoreV1().Nodes().List(ctx, lo)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *realNodeOps) GetNode(ctx context.Context, nodeName string) (*corev1.Node, error) {
	return r.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
}

func (r *realNodeOps) SetNodeCondition(ctx context.Context, nodeName string, condType string, status corev1.ConditionStatus, reason string) error {
	now := metav1.NewTime(time.Now())
	cond := corev1.NodeCondition{
		Type:               corev1.NodeConditionType(condType),
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := r.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		found := false
		conds := latest.Status.Conditions
		for i := range conds {
			if string(conds[i].Type) == condType {
				conds[i] = cond
				found = true
				break
			}
		}
		if !found {
			conds = append(conds, cond)
		}
		latest.Status.Conditions = conds
		_, err = r.client.CoreV1().Nodes().UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}

func enqueueCandidates(ctx context.Context, ops nodeOps, windows []*maintenanceWindow, gs *globalState) error {
	seen := map[string]struct{}{} // avoid duplicates
	for n := range gs.active {
		seen[n] = struct{}{}
	}
	for _, p := range gs.pending {
		seen[p] = struct{}{}
	}

	now := time.Now()
	for _, w := range windows {
		if !windowActive(w, now) {
			continue
		}
		nodes, err := ops.ListNodes(ctx, w.nodeSelector)
		if err != nil {
			// If any error happened during any node listing,
			// we can't be sure the list is complete or accurate.
			// We should not continue to browse all windows and retry later to save the pain to the API server.
			return fmt.Errorf("error list nodes while browsing maintenance window %s, %w", w.name, err)
		}
		for _, n := range nodes {
			if !hasCondition(&n, "NeedsReboot", corev1.ConditionTrue) {
				continue
			}
			if _, ok := seen[n.Name]; ok {
				continue
			}
			gs.pending = append(gs.pending, n.Name)
			seen[n.Name] = struct{}{}
		}
	}
	return nil
}

func processQueue(ctx context.Context, ops nodeOps, gs *globalState, maxRebootConcurrency int) {
	// process pending queue up to concurrency limit
	for len(gs.active) < maxRebootConcurrency && len(gs.pending) > 0 {
		nodeName := gs.pending[0]
		gs.pending = gs.pending[1:]

		// verify node still needs maintenance
		node, err := ops.GetNode(ctx, nodeName)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// Node was deleted, skip it
				fmt.Printf("skipping deleted node %s\n", nodeName)
				continue
			}
			fmt.Fprintf(os.Stderr, "get node %s: %v\n", nodeName, err)
			continue

		}
		if !hasCondition(node, "NeedsReboot", corev1.ConditionTrue) {
			continue
		}

		if err := ops.SetNodeCondition(ctx, nodeName, "kured.dev/UnderMaintenance", corev1.ConditionTrue, "Node moved to active queue"); err != nil {
			fmt.Fprintf(os.Stderr, "set undermaintenance %s: %v\n", nodeName, err)
			continue
		}

		gs.active[nodeName] = struct{}{}
		fmt.Printf("node %s -> maintenance started\n", nodeName)
	}
}

// WatchNodesConditions manages the queues of nodes based on the NeedsReboot condition.
// If NeedsReboot is removed, the node is removed from any queue.
// This allows external actors to remove nodes from maintenance
// e.g., if a node is manually rebooted or patched.
// This runs in a separate goroutine to avoid blocking the main loop.
func WatchNodesConditions(ctx context.Context, ops *realNodeOps, gs *globalState, mu *sync.Mutex) {
	watcher, err := ops.client.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "node watch: %v\n", err)
		return
	}
	defer watcher.Stop()
	for ev := range watcher.ResultChan() {
		if ev.Type == watch.Error {
			continue
		}
		n, ok := ev.Object.(*corev1.Node)
		if !ok {
			continue
		}
		mu.Lock()
		if !hasCondition(n, "NeedsReboot", corev1.ConditionTrue) {
			if _, present := gs.active[n.Name]; present {
				_ = ops.SetNodeCondition(ctx, n.Name, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "NeedsReboot condition removed")
				delete(gs.active, n.Name)
				fmt.Printf("node %s -> maintenance complete\n", n.Name)
			} else {
				for i, nodeName := range gs.pending {
					if nodeName == n.Name {
						gs.pending = append(gs.pending[:i], gs.pending[i+1:]...)
						fmt.Printf("node %s -> removed from pending queue\n", n.Name)
						break
					}
				}
			}
		}
		mu.Unlock()
	}
}

func main() {
	var kubeconfig, cmPrefix, cmNamespace string
	var maxRebootConcurrency int
	var metricsAddr string // kept for parity; not used
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "maintenance configmap prefix")
	flag.StringVar(&cmNamespace, "namespace", "kube-system", "maintenance cm namespace")
	flag.IntVar(&maxRebootConcurrency, "max-reboot-concurrency", 10, "maximum amount of nodes in maintenance at any point in time")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "metrics listen")
	flag.Parse()

	cfg, err := loadKubeConfig(kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kubeconfig: %v\n", err)
		os.Exit(1)
	}
	client := kubernetes.NewForConfigOrDie(cfg)
	ops := &realNodeOps{client: client}

	windows := loadWindows(client, cmNamespace, cmPrefix)
	if len(windows) == 0 {
		fmt.Fprintf(os.Stderr, "no windows found\n")
		os.Exit(1)
	}

	gs := &globalState{
		pending: []string{},
		active:  map[string]struct{}{},
	}

	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go WatchNodesConditions(ctx, ops, gs, &mu)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		_ = now // keep for potential time injection/tests
		mu.Lock()
		if err := enqueueCandidates(ctx, ops, windows, gs); err != nil {
			mu.Unlock()
			fmt.Fprintf(os.Stderr, "enqueue candidates failed: %v\n", err)
			continue
		}
		processQueue(ctx, ops, gs, maxRebootConcurrency)
		mu.Unlock()
	}
}

// helpers:
func loadKubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func loadWindows(client *kubernetes.Clientset, namespace, prefix string) []*maintenanceWindow {
	cms, err := client.CoreV1().ConfigMaps(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "list cms: %v\n", err)
		return nil
	}
	parser := cronlib.NewParser(cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow)
	var res []*maintenanceWindow
	for _, cm := range cms.Items {
		if !strings.HasPrefix(cm.Name, prefix) {
			continue
		}
		d := cm.Data
		cronExpr := strings.TrimSpace(d["startTime"])
		if cronExpr == "" {
			continue
		}
		sched, err := parser.Parse(cronExpr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid cron %s: %v\n", cm.Name, err)
			continue
		}
		durMin, _ := strconv.Atoi(d["duration"])
		res = append(res, &maintenanceWindow{
			name:         cm.Name,
			cronExpr:     cronExpr,
			duration:     time.Duration(durMin) * time.Minute,
			nodeSelector: d["nodeSelector"],
			schedule:     sched,
		})
	}
	return res
}

// windowActive: find last occurrence and compare duration
func windowActive(w *maintenanceWindow, now time.Time) bool {
	start := now.Add(-365 * 24 * time.Hour)
	var last time.Time
	for {
		next := w.schedule.Next(start)
		if next.After(now) {
			break
		}
		last = next
		start = next
	}
	if last.IsZero() {
		return false
	}
	return now.Sub(last) < w.duration
}

func hasCondition(node *corev1.Node, condType string, status corev1.ConditionStatus) bool {
	for _, c := range node.Status.Conditions {
		if string(c.Type) == condType && c.Status == status {
			return true
		}
	}
	return false
}
