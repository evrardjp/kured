package reboot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/kubereboot/kured/internal/conditions"
	"github.com/kubereboot/kured/internal/labels"
	"github.com/kubereboot/kured/internal/taints"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubectl/pkg/drain"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// ControllerName is the name of the controller actually rebooting nodes
	ControllerName = "kured"
)

// Controller reconciles a Node object to trigger a reboot
type Controller struct {
	client.Client
	logger                   *slog.Logger
	scheme                   *runtime.Scheme
	rebooter                 Rebooter
	nodeName                 string
	conditionHeartbeatPeriod time.Duration
	lastProcessedTime        time.Time
	processingMutex          sync.RWMutex
	drainDelay               time.Duration
	drainHelper              *drain.Helper
	recorder                 record.EventRecorder
	preferNoScheduleTaint    *taints.Taint
}

// NewController creates a new instance of the Node controller
func NewController(
	logger *slog.Logger,
	mgr manager.Manager,
	nodeName string,
	rebooter Rebooter,
	heartbeat time.Duration,
	drainDelay time.Duration,
	preferNoScheduleTaint *taints.Taint,
	helper *drain.Helper,
) *Controller {
	c := &Controller{
		Client:                   mgr.GetClient(),
		logger:                   logger,
		scheme:                   mgr.GetScheme(),
		rebooter:                 rebooter,
		nodeName:                 nodeName,
		conditionHeartbeatPeriod: heartbeat,
		lastProcessedTime:        time.Now(),
		drainDelay:               drainDelay,
		preferNoScheduleTaint:    preferNoScheduleTaint,
		recorder:                 mgr.GetEventRecorderFor(ControllerName),
		drainHelper:              helper,
	}

	return c
}

// Reconcile update node conditions based on the maintenance windows
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.logger.Debug("Processing node reconciliation", "node", req.Name)

	// Get the node
	node := &corev1.Node{}
	if err := c.Get(ctx, req.NamespacedName, node); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get node %q: %w", req.Name, err)
	}

	// Check if this is our target node
	if node.Name != c.nodeName {
		return ctrl.Result{}, nil
	}

	// Get the MaintenanceInProgress condition
	inProgressCondition := conditions.GetNodeCondition(node.Status.Conditions, conditions.MaintenanceInProgressConditionType)
	if inProgressCondition == nil {
		RecordReason(c.nodeName, reasonConditionAbsent)
		c.logger.Debug("required condition is absent on the node", "node", node.Name)
		return ctrl.Result{}, nil
	}

	rebootDesired := inProgressCondition.Status == corev1.ConditionTrue
	c.logger.Debug("node condition info", "node", node.Name, "conditionType", conditions.MaintenanceInProgressConditionType, "conditionStatus", rebootDesired, "lastHeartbeatTime", inProgressCondition.LastHeartbeatTime.Time)

	// Lock to prevent concurrent processing
	c.processingMutex.Lock()
	defer c.processingMutex.Unlock()

	nau := labels.NewNodeAnnotationUpdater(c.Client, c.nodeName)

	c.logger.Info("Handling preferNoSchedule taint (if requested)", "node", c.nodeName, "desiredTaintState", map[bool]string{true: "present", false: "absent"}[rebootDesired])
	// Apply/Remove a taint as soon as we enter/leave maintenance
	c.preferNoScheduleTaint.SetState(rebootDesired)

	// Handle node annotations and scheduling state
	previouslyUnschedulable := node.Spec.Unschedulable
	if previouslyUnschedulableAnnotation, ok := node.Annotations[labels.KuredNodeWasUnschedulableBeforeDrainAnnotation]; !ok {
		// No annotation might be fine. Do we need to reboot? Then we need to save Unschedulable spec in annotation.
		if rebootDesired {
			annotations := map[string]string{labels.KuredNodeWasUnschedulableBeforeDrainAnnotation: strconv.FormatBool(node.Spec.Unschedulable)}
			c.logger.Info(fmt.Sprintf("adding annotation %s", labels.KuredNodeWasUnschedulableBeforeDrainAnnotation), "node", c.nodeName)
			if err := nau.AddNodeAnnotations(ctx, annotations); err != nil {
				RecordReason(c.nodeName, reasonAnnotationFailed)
				return ctrl.Result{}, fmt.Errorf("error saving state of the node %s, %v", c.nodeName, err)
			}
		}
	} else {
		// Annotation is present. Restore its content into memory.
		// Never update it here. If an error occurred later in the process, we always want to read the declared user state.
		var err error
		previouslyUnschedulable, err = strconv.ParseBool(previouslyUnschedulableAnnotation)
		if err != nil {
			RecordReason(c.nodeName, reasonAnnotationFailed)
			c.logger.Info("invalid annotation value", "node", c.nodeName, "annotation", labels.KuredNodeWasUnschedulableBeforeDrainAnnotation, "value", previouslyUnschedulableAnnotation)
			// It's worth not continuing, we do not know if we need to uncordon or not.
			// We can resume later or after the user has fixed the annotation. Until then, don't touch.
			return ctrl.Result{}, fmt.Errorf("error recovering state of the node %s, %v", c.nodeName, err)
		}
	}

	// if rebootDesired, then cordon (regardless of previous state)
	// if reboot not desired anymore, then uncordon UNLESS the previous state was already cordonned/unschedulable
	if rebootDesired || !previouslyUnschedulable {
		if errCordon := drain.RunCordonOrUncordon(c.drainHelper, node, rebootDesired); errCordon != nil {
			RecordReason(c.nodeName, reasonCordonFailed)
			return ctrl.Result{}, fmt.Errorf("cordonning node %s failed: %w", c.nodeName, errCordon)
		}
	}

	if rebootDesired {
		c.logger.Info("Draining node", "node", c.nodeName)
		if errDrain := drain.RunNodeDrain(c.drainHelper, c.nodeName); errDrain != nil {
			if errors.Is(errDrain, context.DeadlineExceeded) {
				RecordReason(c.nodeName, reasonDrainTimeout)
			} else {
				RecordReason(c.nodeName, reasonDrainFailed)
			}
			return ctrl.Result{}, fmt.Errorf("error draining node %s: %v", c.nodeName, errDrain)
		}
	} else {
		c.logger.Info("Ensuring absent maintenance annotation", "node", c.nodeName)
		if err := nau.DeleteNodeAnnotation(ctx, labels.KuredNodeWasUnschedulableBeforeDrainAnnotation); err != nil {
			RecordReason(c.nodeName, reasonAnnotationFailed)
			return ctrl.Result{}, fmt.Errorf("error cleaning annotation containing previous Unschedulable state of the node %s, %v", c.nodeName, err)
		}
	}
	c.lastProcessedTime = time.Now()

	if rebootDesired {
		c.logger.Info("Rebooting node", "node", c.nodeName)
		if errR := c.rebooter.Reboot(); errR != nil {
			RecordReason(c.nodeName, reasonRebootFailed)
			return ctrl.Result{}, fmt.Errorf("error rebooting node %s: %w", c.nodeName, errR)
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	pred := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode := e.ObjectOld.(*corev1.Node)
			newNode := e.ObjectNew.(*corev1.Node)

			// Only process events for the target node
			if newNode.Name != c.nodeName {
				return false
			}

			// Check if MaintenanceInProgress condition exists
			inProgressCondition := conditions.GetNodeCondition(newNode.Status.Conditions, conditions.MaintenanceInProgressConditionType)
			if inProgressCondition == nil {
				c.logger.Debug("not processing node object (missing condition)")
				return false
			}

			// Check if status changed or periodic heartbeat required
			oldInProgressCondition := conditions.GetNodeCondition(oldNode.Status.Conditions, conditions.MaintenanceInProgressConditionType)
			if oldInProgressCondition == nil || oldInProgressCondition.Status != inProgressCondition.Status {
				c.logger.Debug("processing node object (status changed)")
				return true
			}

			// Periodic check based on heartbeat
			if time.Since(c.lastProcessedTime) > c.conditionHeartbeatPeriod {
				c.logger.Debug("processing node object (periodic maintenance check)")
				return true
			}

			c.logger.Debug("not processing node object (was already processed recently)")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			node := e.Object.(*corev1.Node)

			// Only process events for the target node
			if node.Name != c.nodeName {
				return false
			}

			// Check if MaintenanceInProgress condition exists
			inProgressCondition := conditions.GetNodeCondition(node.Status.Conditions, conditions.MaintenanceInProgressConditionType)
			if inProgressCondition == nil {
				c.logger.Debug("not processing node object (missing condition)")
				return false
			}

			c.logger.Debug("processing node object (node created)")
			return true
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			// We don't need to handle delete events
			return false
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, builder.WithPredicates(pred)).
		Named(ControllerName).
		Complete(c)
}
