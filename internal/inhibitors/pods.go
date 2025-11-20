package inhibitors

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// FindPods returns a set of node names that cannot be rebooted
func FindPods(ctx context.Context, client *kubernetes.Clientset, podSelectors []string) (NodeSet, error) {
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
