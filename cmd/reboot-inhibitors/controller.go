package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"time"

	"github.com/kubereboot/kured/internal/conditions"
	"github.com/kubereboot/kured/internal/inhibitors"
	"github.com/prometheus/client_golang/api"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func init() {
	prometheus.MustRegister(inhibitors.RebootInhibitedGauge)
}

func InhibitorsWatchLoop(ctx context.Context, ticker *time.Ticker, blockingPodSelectors []string, client *kubernetes.Clientset, prometheusURL string, promClient api.Client, alertFilter *regexp.Regexp, alertFiringOnly bool, alertFilterMatchOnly bool, period time.Duration) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Stopping ticker")
			return
		case <-ticker.C:
			// A new nodeset at every tick will garbage collect all previous nodes, and will avoid requiring our own cleanup
			inhibitedNodes := inhibitors.NewNodeSet()
			if len(blockingPodSelectors) > 0 {
				slog.Debug(fmt.Sprintf("finding inhibitor pods matching %s", blockingPodSelectors))
				protectedNodes, errPods := inhibitors.FindPods(ctx, client, blockingPodSelectors)
				if errPods != nil {
					slog.Error("error finding inhibitor pods", "error", errPods.Error())
					inhibitedNodes.SetDefaults(true, "reboot-inhibitor had an error finding protected pods")
				} else {
					if len(protectedNodes) != 0 {
						slog.Info(fmt.Sprintf("found inhibitor pods matching %s on %s", blockingPodSelectors, slices.Sorted(maps.Keys(protectedNodes))))
						inhibitedNodes.AddBatch(protectedNodes, "reboot-inhibitor found inhibitor pod")
					}
				}
			}
			if prometheusURL != "" {
				slog.Debug("querying prom for alerts")
				matchingAlerts, errProm := inhibitors.FindAlerts(promClient, alertFilter, alertFiringOnly, alertFilterMatchOnly)
				if errProm != nil {
					inhibitedNodes.SetDefaults(true, "an error querying prometheus results in blocking all reboots for now")
					slog.Debug("an error querying prometheus results in blocking all reboots for now", "error", errProm.Error())
				}
				if matchingAlerts != nil && len(matchingAlerts) != 0 {
					inhibitedNodes.SetDefaults(true, "reboot-inhibitor detected an alert in prometheus")
					slog.Info(fmt.Sprintf("reboot-inhibitor detected an alert in prometheus %s", matchingAlerts))
				}
			}
			// Now update nodes
			nodes, errListNodes := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
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
				if err := conditions.UpdateNodeCondition(ctx, client, node.Name, nodeCondition, period); err != nil {
					slog.Error("failed to update node condition", "error", err)
				}
				inhibitors.RebootInhibitedGauge.WithLabelValues(node.Name).Set(inhibitedNodes.MetricValue(node.Name))
			}
		}
	}
}
