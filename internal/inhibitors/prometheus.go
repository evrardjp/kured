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

// FindAlerts returns a list of active alerts' names (e.g. pending or firing)
// alertFiringOnly is a bool to indicate if only firing alerts should be considered
// alertFilterMatchOnly is a bool to indicate that we are only blocking on alerts which match the filter
func FindAlerts(client papi.Client, alertFilter *regexp.Regexp, alertFiringOnly, alertFilterMatchOnly bool) ([]string, error) {
	api := promapiv1.NewAPI(client)

	// get all alerts from prometheus
	value, _, err := api.Query(context.Background(), "ALERTS", time.Now())
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
