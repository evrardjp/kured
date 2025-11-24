package maintenances

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type Window struct {
	Name         string
	Duration     time.Duration
	NodeSelector labels.Selector
	Schedule     string
}

func (w *Window) Checksum() [16]byte {
	data := strings.Join([]string{w.Duration.String(), w.NodeSelector.String(), w.Schedule}, "|")
	return md5.Sum([]byte(data))
}

func NewWindowFromConfigMap(name string, d map[string]string) (*Window, error) {
	if _, errCronParsing := cron.ParseStandard(d["startTime"]); errCronParsing != nil {
		return nil, fmt.Errorf("failed to parse startTime: %w", errCronParsing)
	}
	duration, errConv := time.ParseDuration(d["duration"])
	if errConv != nil {
		return nil, fmt.Errorf("failed to parse duration: %w", errConv)
	}
	var selector labels.Selector
	if nsRaw, ok := d["nodeSelector"]; ok && nsRaw != "" {
		// First, unmarshal YAML or JSON
		var ls v1.LabelSelector
		if err := yaml.Unmarshal([]byte(nsRaw), &ls); err != nil {
			return nil, fmt.Errorf("failed to parse nodeSelector: %w", err)
		}

		// Convert LabelSelector -> labels.Selector
		var errLS error
		selector, errLS = v1.LabelSelectorAsSelector(&ls)
		if errLS != nil {
			return nil, fmt.Errorf("failed to parse nodeSelector: %w", errLS)
		}
	} else {
		selector = labels.Everything()
	}
	return &Window{
		Name:         name,
		Duration:     duration,
		NodeSelector: selector,
		Schedule:     d["startTime"],
	}, nil
}

// FetchConfigmaps loads maintenance windows from the given namespace, using the given prefix.
func FetchConfigmaps(ctx context.Context, client *kubernetes.Clientset, namespace, prefix string) ([]*Window, error) {
	cms, err := client.CoreV1().ConfigMaps(namespace).List(ctx, v1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var res []*Window
	for _, cm := range cms.Items {
		if !strings.HasPrefix(cm.Name, prefix) {
			continue
		}
		d := cm.Data
		window, errCM := NewWindowFromConfigMap(cm.Name, d)
		if errCM != nil {
			return nil, fmt.Errorf("failed to parse ConfigMap %s: %w", cm.Name, errCM)
		}
		res = append(res, window)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("no maintenance window found")
	}
	return res, nil
}
