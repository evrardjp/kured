package maintenances

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	cronlib "github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Window struct {
	name         string
	duration     time.Duration
	nodeSelector string
	schedule     cronlib.Schedule
}

// LoadWindowsOrDie loads maintenance windows from the given namespace, using the given prefix.
// It returns a list of windows if successfully formatted. It will crash if there was an error loading any of them.
func LoadWindowsOrDie(ctx context.Context, client *kubernetes.Clientset, namespace, prefix string) []*Window {
	// TODO, improve by adding listOptions to match a certain label
	cms, err := client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		os.Exit(2)
	}
	parser := cronlib.NewParser(cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow)
	var res []*Window
	for _, cm := range cms.Items {
		if !strings.HasPrefix(cm.Name, prefix) {
			continue
		}
		d := cm.Data
		cronExpr := strings.TrimSpace(d["startTime"])
		sched, err := parser.Parse(cronExpr)
		if err != nil {
			os.Exit(2)
		}
		durMin, errConv := strconv.Atoi(d["duration"])
		if errConv != nil {
			os.Exit(2)
		}
		res = append(res, &Window{
			name:         cm.Name,
			duration:     time.Duration(durMin) * time.Minute,
			nodeSelector: d["nodeSelector"],
			schedule:     sched,
		})
	}
	return res
}

// IsActive is self-explanatory.
// It goes back to maximum a year to find the last occurrence before current Time and then compares duration.
func (w *Window) IsActive(now time.Time) bool {
	start := now.Add(-365 * 24 * time.Hour)
	var last time.Time
	for {
		next := w.schedule.Next(start)
		if next.After(now) {
			break
		}
		last = next
		start = next
	}
	if last.IsZero() {
		return false
	}
	return now.Sub(last) < w.duration
}
