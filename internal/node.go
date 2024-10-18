package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubectldrain "k8s.io/kubectl/pkg/drain"
)

type KuredNode struct {
	name          string
	k8sNodeObject *v1.Node
	k8sClientSet  *kubernetes.Clientset
}

func NewNode(name string, k8sClientSet *kubernetes.Clientset) *KuredNode {
	return &KuredNode{name: name, k8sClientSet: k8sClientSet}
}

func (n *KuredNode) UpdateLabels(labels []string) {
	labelsMap := make(map[string]string)
	for _, label := range labels {
		k := strings.Split(label, "=")[0]
		v := strings.Split(label, "=")[1]
		labelsMap[k] = v
		log.Infof("Updating node %s label: %s=%s", n.name, k, v)
	}

	bytes, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": labelsMap,
		},
	})
	if err != nil {
		log.Fatalf("Error marshalling node object into JSON: %v", err)
	}

	_, err = n.k8sClientSet.CoreV1().Nodes().Patch(context.TODO(), n.name, types.StrategicMergePatchType, bytes, metav1.PatchOptions{})
	if err != nil {
		var labelsErr string
		for _, label := range labels {
			k := strings.Split(label, "=")[0]
			v := strings.Split(label, "=")[1]
			labelsErr += fmt.Sprintf("%s=%s ", k, v)
		}
		log.Errorf("Error updating node labels %s via k8s API: %v", labelsErr, err)
	}
}

func (n *KuredNode) Get() (*v1.Node, error) {
	return n.k8sNodeObject, nil
}

func (n *KuredNode) K8SGet() (*v1.Node, error) {
	node, err := n.k8sClientSet.CoreV1().Nodes().Get(context.TODO(), n.name, metav1.GetOptions{})
	if err != nil || node.GetName() != n.name {
		return &v1.Node{}, fmt.Errorf("error retrieving node object via k8s API %v", err)
	}
	n.k8sNodeObject = node
	return n.k8sNodeObject, nil
}

func (n *KuredNode) Drain(drainDelay time.Duration, drainGracePeriod int, drainPodSelector string, skipWaitForDeleteTimeoutSeconds int, drainTimeout time.Duration, preRebootNodeLabels []string) error {

	if preRebootNodeLabels != nil && len(preRebootNodeLabels) != 0 {
		n.UpdateLabels(preRebootNodeLabels)
	}

	if drainDelay > 0 {
		log.Infof("Delaying drain for %v", drainDelay)
		time.Sleep(drainDelay)
	}

	log.Infof("Draining node %s", n.name)

	//if notifyURL != "" {
	//	if err := shoutrrr.Send(notifyURL, fmt.Sprintf(messageTemplateDrain, nodename)); err != nil {
	//		log.Warnf("Error notifying: %v", err)
	//	}
	//}

	drainer := &kubectldrain.Helper{
		Client:                          n.k8sClientSet,
		Ctx:                             context.Background(),
		GracePeriodSeconds:              drainGracePeriod,
		PodSelector:                     drainPodSelector,
		SkipWaitForDeleteTimeoutSeconds: skipWaitForDeleteTimeoutSeconds,
		Force:                           true,
		DeleteEmptyDirData:              true,
		IgnoreAllDaemonSets:             true,
		ErrOut:                          os.Stderr,
		Out:                             os.Stdout,
		Timeout:                         drainTimeout,
	}

	if err := kubectldrain.RunCordonOrUncordon(drainer, n.k8sNodeObject, true); err != nil {
		log.Errorf("error cordonning %s due to %v", n.name, err)
		return err
	}

	if err := kubectldrain.RunNodeDrain(drainer, n.name); err != nil {
		log.Errorf("error draining %s due to %v", n.name, err)
		return err
	}
	return nil
}

func (n *KuredNode) Uncordon(postRebootNodeLabels ...string) error {
	log.Infof("uncordoning node %s", n.name)

	drainer := &kubectldrain.Helper{
		Client: n.k8sClientSet,
		ErrOut: os.Stderr,
		Out:    os.Stdout,
		Ctx:    context.Background(),
	}
	if n.k8sNodeObject == nil {
		n.K8SGet()
	}
	if err := kubectldrain.RunCordonOrUncordon(drainer, n.k8sNodeObject, false); err != nil {
		log.Fatalf("error uncordonning %s due to %v", n.name, err)
		return err
	}

	if postRebootNodeLabels != nil && len(postRebootNodeLabels) != 0 {
		n.UpdateLabels(postRebootNodeLabels)
	}
	return nil
}
