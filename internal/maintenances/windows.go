package maintenances

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
)

// Windows keeps track of active maintenance windows and their node selectors.
// We do not need to store the maintenance windows directly here, only their selectors for quick lookup.
type Windows struct {
	mu              *sync.Mutex
	activeSelectors map[string]labels.Selector
	// AllWindows is exported to provide access to all defined maintenance windows, not just the active ones. It should be treated as read-only.
	AllWindows map[string]*Window
}

// NewWindows creates a new Windows based on given list of maintenance window instance.
func NewWindows(windows ...*Window) *Windows {
	w := &Windows{
		activeSelectors: map[string]labels.Selector{},
		mu:              &sync.Mutex{},
		AllWindows:      map[string]*Window{},
	}
	for _, window := range windows {
		w.Add(window)
	}
	return w
}

// Add adds a new maintenance window to the collection.
func (a *Windows) Add(window *Window) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.AllWindows[window.Name] = window
}

// Start activates the maintenance window with the given name and node selector.
func (a *Windows) Start(windowName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeSelectors[windowName] = a.AllWindows[windowName].NodeSelector
	ActiveWindowsGauge.WithLabelValues(windowName).Set(1)
}

// End deactivates the maintenance window with the given name.
func (a *Windows) End(windowName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.activeSelectors, windowName)
	ActiveWindowsGauge.WithLabelValues(windowName).Set(0)
}

// MatchesAnyActiveSelector checks if the given node labels match any of the active maintenance window selectors.
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

// String returns a string representation of the active maintenance windows.
func (a *Windows) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var activeWindows []string
	for windowName := range a.activeSelectors {
		activeWindows = append(activeWindows, windowName)
	}
	return strings.Join(activeWindows, " | ")
}

// ListSelectors returns a string representation of the active maintenance window selectors.
func (a *Windows) ListSelectors() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var activeSelectors []string
	for _, selector := range a.activeSelectors {
		activeSelectors = append(activeSelectors, selector.String())
	}
	return strings.Join(activeSelectors, " | ")
}

// Run starts the maintenance window for the specified duration and logs the start and end events.
func (a *Windows) Run(windowName string, logger *slog.Logger) func() {
	return func() {
		logger.Info("Starting maintenance window", "window", windowName)
		a.Start(windowName)
		time.Sleep(a.AllWindows[windowName].Duration)
		logger.Info("End of maintenance window", "window", windowName)
		a.End(windowName)
	}
}
