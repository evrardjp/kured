package inhibitors

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PodInhibitor struct {
	BlockingPodSelectors []string
	Client               *kubernetes.Clientset
}

// Check queries Kubernetes for pods matching the configured selectors, and sets the inhibitedNodes accordingly
// It assumes the caller has already verified that BlockingPodSelectors is non-empty
func (pb *PodInhibitor) Check(ctx context.Context, inhibitedNodes *InhibitedNodeSet) error {
	protectedNodes, errPods := findPods(ctx, pb.Client, pb.BlockingPodSelectors)
	if errPods != nil {
		inhibitedNodes.SetDefaults(true, "reboot-inhibitor had an error finding protected pods")
		return fmt.Errorf("error finding inhibitor pods: %w", errPods)
	} else {
		if len(protectedNodes) != 0 {
			slog.Info(fmt.Sprintf("found inhibitor pods matching %s on %s", pb.BlockingPodSelectors, slices.Sorted(maps.Keys(protectedNodes))))
			inhibitedNodes.AddBatch(protectedNodes, "reboot-inhibitor found inhibitor pod")
		}
	}
	return nil
}

// findPods returns a set of node names that cannot be rebooted
func findPods(ctx context.Context, client *kubernetes.Clientset, podSelectors []string) (NodeSet, error) {
	nodesWithBadPods := make(NodeSet)
	for _, labelSelector := range podSelectors {
		pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
			FieldSelector: "status.phase!=Succeeded,status.phase!=Failed,status.phase!=Unknown",
		})
		if err != nil {
			return nil, fmt.Errorf("pod query error: %w", err)
		}
		if pods == nil {
			return nodesWithBadPods, nil
		}
		for _, pod := range pods.Items {
			nodesWithBadPods[pod.Spec.NodeName] = struct{}{}
		}
	}
	return nodesWithBadPods, nil
}
