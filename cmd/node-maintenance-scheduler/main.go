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
	cronlib "github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

var (
	metrics = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "kured_node_maintenance_status", Help: "1 if maintenance in progress for the node and window, 0 otherwise"},
		[]string{"maintenance_window", "node"},
	)
)

// maintenanceWindow parsed from ConfigMap
type maintenanceWindow struct {
	name         string
	reason       string
	cronExpr     string
	duration     time.Duration
	nodeSelector string
	concurrency  int // kept for info; global concurrency enforced separately
	schedule     cronlib.Schedule
}

// global state
type assignment struct {
	window string
	node   corev1.Node
}

type globalState struct {
	inProgress map[string]assignment // nodeName -> assignment
	sem        *Semaphore            // global semaphore
}

// simple counting semaphore with TryAcquire/Release (test-friendly)
type Semaphore struct {
	ch chan struct{}
}

func NewSemaphore(n int) *Semaphore {
	if n <= 0 {
		n = 1
	}
	return &Semaphore{ch: make(chan struct{}, n)}
}

func (s *Semaphore) TryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Semaphore) Release() {
	select {
	case <-s.ch:
	default:
	}
}

// nodeOps abstraction for testability
type nodeOps interface {
	ListNodes(ctx context.Context, selector string) ([]corev1.Node, error)
	SetNodeCondition(ctx context.Context, nodeName string, condType string, status corev1.ConditionStatus, reason string) error
	GetNode(ctx context.Context, nodeName string) (*corev1.Node, error)
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

// ReconcileGlobal builds a global view: for each active window collect candidate nodes,
// deduplicate nodes (node -> first matching window by iteration order), and assign up to global slots.
func ReconcileGlobal(ctx context.Context, now time.Time, ops nodeOps, windows []*maintenanceWindow, gs *globalState) error {
	// mark which windows are active
	active := map[string]*maintenanceWindow{}
	for _, w := range windows {
		if windowActive(w, now) {
			active[w.name] = w
		}
	}

	// release assignments whose window ended
	for nodeName, a := range gs.inProgress {
		if _, winStillActive := active[a.window]; !winStillActive {
			_ = ops.SetNodeCondition(ctx, nodeName, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "window-ended")
			metrics.WithLabelValues(a.window, nodeName).Set(0)
			delete(gs.inProgress, nodeName)
			gs.sem.Release()
		}
	}

	// collect candidates per active window; deduplicate by node name picking first window encountered
	candidates := map[string]*maintenanceWindow{} // nodeName -> window
	for _, w := range active {
		nodes, err := ops.ListNodes(ctx, w.nodeSelector)
		if err != nil {
			// best-effort: log and continue
			fmt.Fprintf(os.Stderr, "list nodes for %s: %v\n", w.name, err)
			continue
		}
		for _, n := range nodes {
			if !hasCondition(&n, "NeedsReboot", corev1.ConditionTrue) {
				continue
			}
			if _, assigned := gs.inProgress[n.Name]; assigned {
				continue // already in progress
			}
			if _, seen := candidates[n.Name]; seen {
				continue // already claimed by another window (first-wins)
			}
			candidates[n.Name] = w
		}
	}

	// assign up to global capacity
	for nodeName, w := range candidates {
		if !gs.sem.TryAcquire() {
			break // global concurrency reached
		}
		// fetch node to set condition (or rely on candidate listing object if available in real code)
		n, err := ops.GetNode(ctx, nodeName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "get node %s: %v\n", nodeName, err)
			gs.sem.Release()
			continue
		}
		if err := ops.SetNodeCondition(ctx, nodeName, "kured.dev/UnderMaintenance", corev1.ConditionTrue, w.reason); err != nil {
			fmt.Fprintf(os.Stderr, "set undermaintenance %s: %v\n", nodeName, err)
			gs.sem.Release()
			continue
		}
		gs.inProgress[nodeName] = assignment{window: w.name, node: *n}
		metrics.WithLabelValues(w.name, nodeName).Set(1)
		fmt.Printf("assigned node %s -> window %s\n", nodeName, w.name)
	}
	return nil
}

func main() {
	var kubeconfig, cmPrefix, metricsAddr, cmNamespace string
	var globalConcurrency int
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "configmap prefix")
	flag.StringVar(&metricsAddr, "metrics-addr", ":9090", "metrics listen")
	flag.StringVar(&cmNamespace, "namespace", "kube-system", "cm namespace")
	flag.IntVar(&globalConcurrency, "global-concurrency", 10, "global max concurrent nodes in maintenance")
	flag.Parse()

	prometheus.MustRegister(metrics)
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		_ = http.ListenAndServe(metricsAddr, nil)
	}()

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
		inProgress: map[string]assignment{},
		sem:        NewSemaphore(globalConcurrency),
	}

	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// watch nodes to free slots when NeedsReboot -> False
	go func() {
		watcher, err := client.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{})
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
			if a, present := gs.inProgress[n.Name]; present {
				if !hasCondition(n, "NeedsReboot", corev1.ConditionTrue) {
					_ = ops.SetNodeCondition(context.Background(), n.Name, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "reboot-complete")
					metrics.WithLabelValues(a.window, n.Name).Set(0)
					delete(gs.inProgress, n.Name)
					gs.sem.Release()
					fmt.Printf("node %s left maintenance (reboot-complete)\n", n.Name)
				}
			}
			mu.Unlock()
		}
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		mu.Lock()
		_ = ReconcileGlobal(ctx, now, ops, windows, gs)
		mu.Unlock()
	}
}

// helpers (loadKubeConfig, loadWindows, windowActive, hasCondition) similar to previous examples:

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
		concur := 1
		if s := d["concurrency"]; s != "" {
			if v, err := strconv.Atoi(s); err == nil {
				concur = v
			}
		}
		res = append(res, &maintenanceWindow{
			name:         cm.Name,
			reason:       d["reason"],
			cronExpr:     cronExpr,
			duration:     time.Duration(durMin) * time.Minute,
			nodeSelector: d["nodeSelector"],
			concurrency:  concur,
			schedule:     sched,
		})
	}
	return res
}

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
