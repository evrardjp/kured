package labels

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// KuredNodeWasUnschedulableBeforeDrainAnnotation contains is the key where kured stores whether the node was unschedulable before the maintenance.
	KuredNodeWasUnschedulableBeforeDrainAnnotation string = "kured.dev/node-unschedulable-before-drain"
)

// NodeAnnotationUpdater handles node annotation operations
type NodeAnnotationUpdater struct {
	client   client.Client
	nodeName string
}

func NewNodeAnnotationUpdater(cl client.Client, nodeName string) *NodeAnnotationUpdater {
	return &NodeAnnotationUpdater{
		client:   cl,
		nodeName: nodeName,
	}
}

func (u *NodeAnnotationUpdater) updateAnnotations(ctx context.Context, updateFunc func(map[string]string)) error {
	currentNode := &corev1.Node{}
	if err := u.client.Get(ctx, types.NamespacedName{Name: u.nodeName}, currentNode); err != nil {
		return fmt.Errorf("error retrieving node object via k8s API: %v", err)
	}

	newNode := currentNode.DeepCopy()

	if newNode.Annotations == nil {
		newNode.Annotations = make(map[string]string)
	}

	updateFunc(newNode.Annotations)

	patch := client.StrategicMergeFrom(currentNode)
	if err := u.client.Patch(ctx, newNode, patch); err != nil {
		return fmt.Errorf("error updating node annotations via k8s API: %v", err)
	}

	return nil
}

// AddNodeAnnotations adds or updates annotations on a node
func (u *NodeAnnotationUpdater) AddNodeAnnotations(ctx context.Context, annotations map[string]string) error {
	return u.updateAnnotations(ctx, func(nodeAnnotations map[string]string) {
		for k, v := range annotations {
			nodeAnnotations[k] = v
		}
	})
}

// DeleteNodeAnnotation removes a specific annotation from a node
func (u *NodeAnnotationUpdater) DeleteNodeAnnotation(ctx context.Context, key string) error {
	return u.updateAnnotations(ctx, func(nodeAnnotations map[string]string) {
		delete(nodeAnnotations, key)
	})
}
