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

type maintenanceWindow struct {
	name         string
	reason       string
	cronExpr     string
	duration     time.Duration
	nodeSelector string
	concurrency  int
	schedule     cronlib.Schedule
}

func main() {
	var kubeconfig, cmPrefix, metricsAddr, cmNamespace string
	flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "configmap prefix")
	flag.StringVar(&metricsAddr, "metrics-addr", ":9090", "metrics listen")
	flag.StringVar(&cmNamespace, "namespace", "kube-system", "cm namespace")
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

	// load windows once (you can watch ConfigMaps to refresh)
	windows := loadWindows(client, cmNamespace, cmPrefix)
	if len(windows) == 0 {
		fmt.Fprintf(os.Stderr, "no windows found\n")
		os.Exit(1)
	}

	// shared state across windows
	type windowState struct {
		inProgress map[string]corev1.Node
		sem        chan struct{}
	}
	states := map[string]*windowState{}
	for _, w := range windows {
		states[w.name] = &windowState{
			inProgress: map[string]corev1.Node{},
			sem:        make(chan struct{}, w.concurrency),
		}
	}

	var mu sync.Mutex

	// start node watch to detect NeedsReboot -> False
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { watchNodes(ctx, client, windows, &mu, states) }()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for now := range ticker.C {
		mu.Lock()
		for _, w := range windows {
			if !windowActive(w, now) {
				// close out active nodes when window ended
				for nodename, n := range states[w.name].inProgress {
					_ = setNodeCondition(context.Background(), client, &n, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "window-ended")
					metrics.WithLabelValues(w.name, nodename).Set(0)
					delete(states[w.name].inProgress, nodename)
					select {
					case <-states[w.name].sem:
					default:
					}
				}
				continue
			}
			// list nodes matching selector and NeedsReboot=true
			lo := metav1.ListOptions{}
			if w.nodeSelector != "" {
				lo.LabelSelector = w.nodeSelector
			}
			nodes, err := client.CoreV1().Nodes().List(context.Background(), lo)
			if err != nil {
				fmt.Fprintf(os.Stderr, "list nodes: %v\n", err)
				continue
			}
			for _, n := range nodes.Items {
				if !hasCondition(&n, "NeedsReboot", corev1.ConditionTrue) {
					continue
				}
				if _, ok := states[w.name].inProgress[n.Name]; ok {
					continue
				}
				// if slot available, set under maintenance and mark inProgress
				if len(states[w.name].inProgress) < cap(states[w.name].sem) {
					if err := setNodeCondition(context.Background(), client, &n, "kured.dev/UnderMaintenance", corev1.ConditionTrue, w.reason); err != nil {
						fmt.Fprintf(os.Stderr, "set undermaintenance: %v\n", err)
						continue
					}
					states[w.name].inProgress[n.Name] = n
					states[w.name].sem <- struct{}{}
					metrics.WithLabelValues(w.name, n.Name).Set(1)
					fmt.Printf("window %s: node %s -> under maintenance\n", w.name, n.Name)
				}
			}
		}
		mu.Unlock()
	}
}

// Helpers (parsing CM, cron, node watch, condition helpers). Keep minimal.

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

// windowActive returns true if 'now' is within the last cron start + duration.
func windowActive(w *maintenanceWindow, now time.Time) bool {
	// find last occurrence by stepping from a lookback point
	start := now.Add(-365 * 24 * time.Hour)
	var last time.Time
	for {
		next := w.schedule.Next(start)
		if next.After(now) {
			break
		}
		last = next
		start = next
		// safety: stop if we loop too many times (shouldn't)
	}
	if last.IsZero() {
		return false
	}
	return now.Sub(last) < w.duration
}

func watchNodes(ctx context.Context, client *kubernetes.Clientset, windows []*maintenanceWindow, mu *sync.Mutex, states map[string]*windowState) {
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
		for _, w := range windows {
			st := states[w.name]
			if st == nil {
				continue
			}
			if _, present := st.inProgress[n.Name]; present {
				if !hasCondition(n, "NeedsReboot", corev1.ConditionTrue) {
					// done
					_ = setNodeCondition(context.Background(), client, n, "kured.dev/UnderMaintenance", corev1.ConditionFalse, "reboot-complete")
					metrics.WithLabelValues(w.name, n.Name).Set(0)
					delete(st.inProgress, n.Name)
					select {
					case <-st.sem:
					default:
					}
					fmt.Printf("window %s: node %s left maintenance\n", w.name, n.Name)
				}
			}
		}
		mu.Unlock()
	}
}

func hasCondition(node *corev1.Node, condType string, status corev1.ConditionStatus) bool {
	for _, c := range node.Status.Conditions {
		if string(c.Type) == condType && c.Status == status {
			return true
		}
	}
	return false
}

func setNodeCondition(ctx context.Context, client *kubernetes.Clientset, node *corev1.Node, condType string, status corev1.ConditionStatus, reason string) error {
	now := metav1.NewTime(time.Now())
	cond := corev1.NodeCondition{
		Type:               corev1.NodeConditionType(condType),
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := client.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
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
		_, err = client.CoreV1().Nodes().UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}
