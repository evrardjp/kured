package rebootblockers

import (
	"context"
	"fmt"
	"regexp"

	"github.com/prometheus/common/log"
	"github.com/weaveworks/kured/pkg/alerts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RebootBlocker interface should be implemented by types
// to know if their instantiations should block a reboot
type RebootBlocker interface {
	isBlocked() bool
}

// PrometheusBlockingChecker contains info for connecting
// to prometheus, and can give info about whether a reboot should be blocked
type PrometheusBlockingChecker struct {
	// URL to contact prometheus API for checking alerts
	PromURL string
	// regexp used to get alerts
	Filter *regexp.Regexp
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

func (pb PrometheusBlockingChecker) isBlocked() bool {
	alertNames, err := alerts.PrometheusActiveAlerts(pb.PromURL, pb.Filter)
	if err != nil {
		log.Warnf("Reboot blocked: prometheus query error: %v", err)
		return true
	}
	count := len(alertNames)
	if count > 10 {
		alertNames = append(alertNames[:10], "...")
	}
	if count > 0 {
		log.Warnf("Reboot blocked: %d active alerts: %v", count, alertNames)
		return true
	}
	return false
}

func (kb KubernetesBlockingChecker) isBlocked() bool {
	fieldSelector := fmt.Sprintf("spec.nodeName=%s", kb.Nodename)
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

func IsRebootBlocked(blockers ...RebootBlocker) bool {
	for _, blocker := range blockers {
		if blocker.isBlocked() {
			return true
		}
	}
	return false
}
