package maintenances

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
		Name:      "active_maintenance_windows",
		Help:      "maintenance window is active if its value is 1",
	}, []string{"window"})
)

type Window struct {
	Name         string
	Duration     time.Duration
	NodeSelector labels.Selector
	Schedule     string
}

// FetchWindows loads maintenance windows from the given namespace, using the given prefix.
func FetchWindows(ctx context.Context, client *kubernetes.Clientset, namespace, prefix string) ([]*Window, error) {
	cms, err := client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var res []*Window
	for _, cm := range cms.Items {
		if !strings.HasPrefix(cm.Name, prefix) {
			continue
		}
		d := cm.Data
		window, err := parseMaintenanceConfigMap(cm.Name, d)
		if err != nil {
			return nil, err
		}
		slog.Debug("maintenance window details", "window", window.Name, "schedule", window.Schedule, "duration", window.Duration.String(), "nodeSelector", window.NodeSelector.String())
		res = append(res, window)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("no windows found")
	}
	return res, nil
}

func parseMaintenanceConfigMap(name string, d map[string]string) (*Window, error) {
	if _, errCronParsing := cronlib.ParseStandard(d["startTime"]); errCronParsing != nil {
		return nil, errCronParsing
	}
	duration, errConv := time.ParseDuration(d["duration"])
	if errConv != nil {
		return nil, errConv
	}
	var selector labels.Selector
	if nsRaw, ok := d["nodeSelector"]; ok && nsRaw != "" {
		// First, unmarshal YAML or JSON
		var ls metav1.LabelSelector
		if err := yaml.Unmarshal([]byte(nsRaw), &ls); err != nil {
			return nil, err
		}

		// Convert LabelSelector -> labels.Selector
		var errLS error
		selector, errLS = metav1.LabelSelectorAsSelector(&ls)
		if errLS != nil {
			return nil, errLS
		}
	} else {
		selector = labels.Everything()
	}
	return &Window{
		Name:         name,
		Duration:     duration,
		NodeSelector: selector,
		Schedule:     d["startTime"],
	}, nil
}

// Windows keeps track of active maintenance windows and their node selectors.
// We do not need to store the maintenance windows directly here, only their selectors for quick lookup.
type Windows struct {
	mu              *sync.Mutex
	activeSelectors map[string]labels.Selector
	// AllWindows is exported to provide access to all defined maintenance windows, not just the active ones. It should be treated as read-only.
	AllWindows map[string]*Window
}

func NewWindows(windows []*Window) *Windows {
	w := &Windows{
		activeSelectors: map[string]labels.Selector{},
		mu:              &sync.Mutex{},
		AllWindows:      map[string]*Window{},
	}
	for _, window := range windows {
		w.AllWindows[window.Name] = window
	}
	return w
}

// Start activates the maintenance window with the given name and node selector.
func (a *Windows) Start(windowName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeSelectors[windowName] = a.AllWindows[windowName].NodeSelector
	ActiveMaintenanceWindowGauge.WithLabelValues(windowName).Set(1)
}

// End deactivates the maintenance window with the given name.
func (a *Windows) End(windowName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.activeSelectors, windowName)
	ActiveMaintenanceWindowGauge.WithLabelValues(windowName).Set(0)
}

func (a *Windows) MatchesAnyActiveSelector(nodeLabels map[string]string) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for mwName, selector := range a.activeSelectors {
		if selector.Matches(labels.Set(nodeLabels)) {
			return true, mwName
		}
	}
	return false, ""
}

func (a *Windows) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var activeWindows []string
	for windowName := range a.activeSelectors {
		activeWindows = append(activeWindows, windowName)
	}
	return strings.Join(activeWindows, " | ")
}

func (a *Windows) ListSelectors() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var activeSelectors []string
	for _, selector := range a.activeSelectors {
		activeSelectors = append(activeSelectors, selector.String())
	}
	return strings.Join(activeSelectors, " | ")
}
