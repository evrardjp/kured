package blockers

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Compile-time checks to ensure all types implement fmt.Stringer and isBlocked()
var (
	_ RebootBlocker = (*PrometheusBlockingChecker)(nil)
	_ RebootBlocker = (*KubernetesBlockingChecker)(nil)
)

// RebootBlocked checks that a single block Checker
// will block the reboot or not.
func RebootBlocked(blockers ...RebootBlocker) (blocked bool, blockernames []string) {
	for _, blocker := range blockers {
		if blocker.IsBlocked() {
			blocked = true
			blockernames = append(blockernames, fmt.Sprintf("%s", blocker))
		}
	}
	return
}

// RebootBlocker interface should be implemented by types
// to know if their instantiations should block a reboot
// As blockers are now exported as labels, the blocker must
// implement a MetricLabel method giving a label for the
// blocker, with low cardinality if possible.
type RebootBlocker interface {
	IsBlocked() bool
	MetricLabel() string
}

// KubernetesBlockingChecker contains info for connecting
// to k8s, and can give info about whether a reboot should be blocked
type KubernetesBlockingChecker struct {
	// client used to contact kubernetes API
	Client   *kubernetes.Clientset
	Nodename string
	// lised used to filter pods (podSelector)
	Filter []string
}

// IsBlocked for the KubernetesBlockingChecker will check if a pod, for the node, is preventing
// the reboot. It will warn in the logs about blocking, but does not return an error.
func (kb KubernetesBlockingChecker) IsBlocked() bool {
	fieldSelector := fmt.Sprintf("spec.nodeName=%s,status.phase!=Succeeded,status.phase!=Failed,status.phase!=Unknown", kb.Nodename)
	for _, labelSelector := range kb.Filter {
		podList, err := kb.Client.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector,
			FieldSelector: fieldSelector,
			Limit:         10})
		if err != nil {
			log.Warnf("Reboot blocked: pod query error: %v", err)
			return true
		}

		if len(podList.Items) > 0 {
			podNames := make([]string, 0, len(podList.Items))
			for _, pod := range podList.Items {
				podNames = append(podNames, pod.Name)
			}
			if len(podList.Continue) > 0 {
				podNames = append(podNames, "...")
			}
			log.Warnf("Reboot blocked: matching pods: %v", podNames)
			return true
		}
	}
	return false
}

func (kb KubernetesBlockingChecker) MetricLabel() string {
	return "pod"
}
