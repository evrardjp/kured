package conditions

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func ListAllNodesWithConditionType(ctx context.Context, clientSet *kubernetes.Clientset, conditionType string) ([]string, error) {

	allNodes, err := clientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	// Find nodes that match all criteria
	var matchingNodes []string
	for _, node := range allNodes.Items {
		if hasCondition, _ := Matches(node.Status.Conditions, []string{conditionType}, []string{}); hasCondition {
			matchingNodes = append(matchingNodes, node.Name)
		}
	}
	return matchingNodes, nil
}
