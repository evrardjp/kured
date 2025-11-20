package maintenances

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/labels"
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

func (a *Windows) Run(windowName string, logger *slog.Logger) func() {
	return func() {
		logger.Info("Starting maintenance window", "window", windowName)
		a.Start(windowName)
		time.Sleep(a.AllWindows[windowName].Duration)
		logger.Info("End of maintenance window", "window", windowName)
		a.End(windowName)
	}
}
