package maintenances

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// Window represents a maintenance window configuration.
type Window struct {
	Name         string
	Duration     time.Duration
	NodeSelector labels.Selector
	Schedule     string
}

// Checksum computes a SHA-256 checksum of the window's configuration.
// This can be used to detect changes in the configuration.
// The checksum is computed over the Duration, NodeSelector, and Schedule fields.
// While we do not need cryptographic hashing, we use SHA-256 for linting convenience.
func (w *Window) Checksum() [32]byte {
	data := strings.Join([]string{w.Duration.String(), w.NodeSelector.String(), w.Schedule}, "|")
	return sha256.Sum256([]byte(data))
}

// NewWindowFromConfigMap creates a Window from the given ConfigMap data.
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
