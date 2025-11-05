package maintenances

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	cronlib "github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

var (
	ActiveMaintenanceWindowGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "kured",
		Name:      "active_maintenance_windoww",
		Help:      "maintenance window is active if its value is 1",
	}, []string{"window"})
)

type Window struct {
	Name         string
	Duration     time.Duration
	NodeSelector labels.Selector
	Schedule     string
}

// LoadWindowsOrDie loads maintenance windows from the given namespace, using the given prefix.
// It returns a list of windows if successfully formatted. It will crash if there was an error loading any of them.
func LoadWindowsOrDie(ctx context.Context, client *kubernetes.Clientset, namespace, prefix string) []*Window {
	// TODO, improve by adding listOptions to match a certain label
	cms, err := client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		os.Exit(2)
	}
	parser := cronlib.NewParser(cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow)
	var res []*Window
	for _, cm := range cms.Items {
		if !strings.HasPrefix(cm.Name, prefix) {
			continue
		}
		d := cm.Data
		cronExpr := strings.TrimSpace(d["startTime"])
		if _, err := parser.Parse(cronExpr); err != nil {
			os.Exit(2)
		}
		durMin, errConv := strconv.Atoi(d["duration"])
		if errConv != nil {
			os.Exit(2)
		}
		var selector labels.Selector
		if nsRaw, ok := d["nodeSelector"]; ok && nsRaw != "" {
			// First, unmarshal YAML or JSON
			var ls metav1.LabelSelector
			if err := yaml.Unmarshal([]byte(nsRaw), &ls); err != nil {
				os.Exit(2)
			}

			// Convert LabelSelector -> labels.Selector
			selector, err = metav1.LabelSelectorAsSelector(&ls)
			if err != nil {
				os.Exit(2)
			}
		} else {
			selector = labels.Everything()
		}
		res = append(res, &Window{
			Name:         cm.Name,
			Duration:     time.Duration(durMin) * time.Minute,
			NodeSelector: selector,
			Schedule:     d["startTime"],
		})
	}
	return res
}

//
//// IsActive is self-explanatory.
//// It goes back to maximum a year to find the last occurrence before current Time and then compares duration.
//func (w *Window) IsActive(now time.Time) bool {
//	start := now.Add(-365 * 24 * time.Hour)
//	var last time.Time
//	for {
//		next := w.Schedule.Next(start)
//		if next.After(now) {
//			break
//		}
//		last = next
//		start = next
//	}
//	if last.IsZero() {
//		return false
//	}
//	return now.Sub(last) < w.Duration
//}
//
//func (w *Window) ContainsNode(nodeLabels map[string]string) bool {
//	return w.NodeSelector.Matches(labels.Set(nodeLabels))
//}
