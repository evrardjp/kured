package inhibitors

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"

	papi "github.com/prometheus/client_golang/api"
	promapiv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// PrometheusInhibitor queries a Prometheus server for active alerts
// It is a global inhibitor - if any matching alerts are found, all reboots are inhibited
type PrometheusInhibitor struct {
	PromClient           papi.Client
	PrometheusURL        string
	AlertFilter          *regexp.Regexp
	AlertFiringOnly      bool
	AlertFilterMatchOnly bool
}

// Check queries Prometheus for active alerts, and sets the inhibitedNodes accordingly
// it assumes the caller has already verified that PrometheusURL is non-empty
func (pi *PrometheusInhibitor) Check(ctx context.Context, inhibitedNodes *InhibitedNodeSet) error {
	matchingAlerts, errProm := findAlerts(ctx, pi.PromClient, pi.AlertFilter, pi.AlertFiringOnly, pi.AlertFilterMatchOnly)
	if errProm != nil {
		inhibitedNodes.SetDefaults(true, "an error querying prometheus results in blocking all reboots for now")
		return fmt.Errorf("blocking all reboots for now due to an error querrying prom %w", errProm)
	}
	if len(matchingAlerts) != 0 {
		inhibitedNodes.SetDefaults(true, "reboot-inhibitor detected an alert in prometheus")
	}
	return nil
}

// findAlerts returns a list of active alerts' names (e.g. pending or firing)
// alertFiringOnly is a bool to indicate if only firing alerts should be considered
// alertFilterMatchOnly is a bool to indicate that we are only blocking on alerts which match the filter
func findAlerts(ctx context.Context, client papi.Client, alertFilter *regexp.Regexp, alertFiringOnly, alertFilterMatchOnly bool) ([]string, error) {
	api := promapiv1.NewAPI(client)

	// get all alerts from prometheus
	value, _, err := api.Query(ctx, "ALERTS", time.Now())
	if err != nil {
		return nil, err
	}

	if value.Type() == model.ValVector {
		if vector, ok := value.(model.Vector); ok {
			activeAlertSet := make(map[string]bool)
			for _, sample := range vector {
				if alertName, isAlert := sample.Metric[model.AlertNameLabel]; isAlert && sample.Value != 0 {
					if matchesRegex(alertFilter, string(alertName), alertFilterMatchOnly) && (!alertFiringOnly || sample.Metric["alertstate"] == model.LabelValue(promapiv1.AlertStateFiring)) {
						activeAlertSet[string(alertName)] = true
					}
				}
			}

			var activeAlerts []string
			for activeAlert := range activeAlertSet {
				activeAlerts = append(activeAlerts, activeAlert)
			}
			sort.Strings(activeAlerts)

			return activeAlerts, nil
		}
	}

	return nil, fmt.Errorf("unexpected value type %v", value)
}

func matchesRegex(filter *regexp.Regexp, alertName string, filterMatchOnly bool) bool {
	if filter == nil {
		return true
	}

	return filter.MatchString(alertName) == filterMatchOnly
}
