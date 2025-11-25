package inhibitors

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/kubereboot/kured/internal/conditions"
	papi "github.com/prometheus/client_golang/api"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func init() {
	prometheus.MustRegister(RebootInhibitedGauge)
}

// Config holds the configuration for the Reboot Inhibitors "MaintainCondition" controller.
type Config struct {
	Ticker     *time.Ticker
	Client     *kubernetes.Clientset
	Logger     *slog.Logger
	Inhibitors []Inhibitor
	Period     time.Duration
}

// NewControllerConfig creates a new Config for the Reboot Inhibitors controller based on the provided parameters, removing any useless inhibitors.
func NewControllerConfig(ticker *time.Ticker, client *kubernetes.Clientset, logger *slog.Logger, blockingPodSelectors []string, prometheusURL string, alertFilterRegexp *regexp.Regexp, alertFiringOnly, alertFilterMatchOnly bool, period time.Duration) (*Config, error) {
	var inhibitors []Inhibitor
	if len(blockingPodSelectors) > 0 {
		inhibitors = append(inhibitors, &PodInhibitor{
			BlockingPodSelectors: blockingPodSelectors,
			Client:               client,
		})
	}
	if prometheusURL != "" {
		// Core initialisation, error 1 on failure
		promClient, err := papi.NewClient(papi.Config{Address: prometheusURL})
		if err != nil {
			return &Config{}, fmt.Errorf("failed to create Prometheus client %w", err)
		}
		inhibitors = append(inhibitors, &PrometheusInhibitor{
			PromClient:           promClient,
			PrometheusURL:        prometheusURL,
			AlertFilter:          alertFilterRegexp,
			AlertFiringOnly:      alertFiringOnly,
			AlertFilterMatchOnly: alertFilterMatchOnly,
		})
	}
	return &Config{
		Ticker:     ticker,
		Period:     period,
		Client:     client,
		Logger:     logger,
		Inhibitors: inhibitors,
	}, nil
}

// MaintainCondition runs the main loop for checking reboot inhibitors and updating node conditions accordingly.
func MaintainCondition(ctx context.Context, config *Config) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Stopping ticker")
			return
		case <-config.Ticker.C:
			// A new nodeset at every tick will garbage collect all previous nodes, and will avoid requiring our own cleanup
			inhibitedNodes := NewNodeSet()
			for _, inhibitor := range config.Inhibitors {
				if err := inhibitor.Check(ctx, inhibitedNodes); err != nil {
					slog.Warn("inhibitor check failed. preventing all reboots", "error", err)
				}
			}
			// Now update nodes
			nodes, errListNodes := config.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if errListNodes != nil {
				slog.Warn("will not act due to an error occurred while listing nodes", "error", errListNodes.Error())
				continue
			}
			for _, node := range nodes.Items {
				nodeCondition := corev1.NodeCondition{
					Type:               conditions.StringToConditionType(conditions.InhibitedRebootConditionType),
					Status:             conditions.BoolToConditionStatus(inhibitedNodes.Status(node.Name)),
					Reason:             conditions.InhibitedRebootConditionReason,
					Message:            inhibitedNodes.Message(node.Name),
					LastHeartbeatTime:  metav1.Now(),
					LastTransitionTime: metav1.Now(),
				}
				if err := conditions.UpdateNodeCondition(ctx, config.Client, node.Name, nodeCondition, config.Period); err != nil {
					slog.Error("failed to update node condition", "error", err)
				}
				RebootInhibitedGauge.WithLabelValues(node.Name).Set(inhibitedNodes.MetricValue(node.Name))
			}
		}
	}
}
