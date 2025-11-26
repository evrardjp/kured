package maintenances

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kubereboot/kured/internal/conditions"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	MaintenanceWindowNodeControllerName = "maintenance-scheduler-node"
	MaintenanceWindowCMControllerName   = "maintenance-scheduler-cm"
)

// MaintenanceSchedulerNodeReconciler reconciles a Node object for all the maintenances windows conditions
type MaintenanceSchedulerNodeReconciler struct {
	client.Client
	Scheme                    *runtime.Scheme
	MaintenanceWindows        *Windows
	ConditionHeartbeatPeriod  time.Duration
	Logger                    *slog.Logger
	RequiredConditionTypes    []string
	ForbiddenConditionTypes   []string
	MaximumNodesInMaintenance int
}

// Reconcile update node conditions based on the maintenance windows
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (r *MaintenanceSchedulerNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	//logger := logf.FromContext(ctx)
	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		if k8serrors.IsNotFound(err) {
			// If the custom resource is not found then it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	MWCondition := r.prepareMWCondition(&node)

	MIPCondition, err := r.prepareMIPCondition(ctx, &node)
	if err != nil {
		return ctrl.Result{}, err
	}
	NodesInProgressGauge.WithLabelValues(node.GetName()).Set(map[bool]float64{true: 1.0, false: 0.0}[MIPCondition.Status == corev1.ConditionTrue])

	var conditionsToApply []corev1.NodeCondition
	conditionsToApply = append(conditionsToApply, MWCondition, MIPCondition)
	conditionsToApply = append(conditionsToApply, MWCondition)

	return r.reconcileConditions(ctx, &node, conditionsToApply)
}

// SetupWithManager sets up the controller with the Manager.
func (r *MaintenanceSchedulerNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Named(MaintenanceWindowNodeControllerName).
		Complete(r)
}

// Managed the Maintenance Window Condition for the node
func (r *MaintenanceSchedulerNodeReconciler) prepareMWCondition(node *corev1.Node) corev1.NodeCondition {
	underMaintenance, mwName := r.MaintenanceWindows.MatchesAnyActiveSelector(node.Labels)
	conditionToApply := corev1.NodeCondition{
		Type:               conditions.StringToConditionType(conditions.UnderMaintenanceConditionType),
		Status:             conditions.BoolToConditionStatus(underMaintenance),
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
	}
	if mwName == "" || !underMaintenance {
		conditionToApply.Reason = conditions.UnderNoMaintenanceConditionReason
		conditionToApply.Message = "Node is not matching any maintenance window configmap"
	} else {
		conditionToApply.Reason = conditions.UnderMaintenanceConditionReason
		conditionToApply.Message = fmt.Sprintf("Config map %s is putting node under maintenance", mwName)
	}
	return conditionToApply
}

func (r *MaintenanceSchedulerNodeReconciler) prepareMIPCondition(ctx context.Context, node *corev1.Node) (corev1.NodeCondition, error) {
	forbiddenMet, forbiddenCondition := r.checkForbiddenConditions(node)
	if forbiddenMet {
		return forbiddenCondition, nil
	}

	requiredMet, unmetRequirementCondition := r.checkRequiredConditions(node)
	if !requiredMet {
		return unmetRequirementCondition, nil
	}

	if canEnterMaintenance, cannotEnterCondition, err := r.checkConcurrencyLimit(ctx, node); err != nil {
		return corev1.NodeCondition{}, err
	} else {
		if !canEnterMaintenance {
			return cannotEnterCondition, nil
		}
	}

	return corev1.NodeCondition{
		Type:               conditions.StringToConditionType(conditions.MaintenanceInProgressConditionType),
		Status:             conditions.BoolToConditionStatus(true),
		Reason:             conditions.MaintenanceInProgressConditionSuccessReason,
		Message:            "All conditions required are present to start the node maintenance",
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
	}, nil
}

func (r *MaintenanceSchedulerNodeReconciler) checkForbiddenConditions(node *corev1.Node) (bool, corev1.NodeCondition) {
	if len(r.ForbiddenConditionTypes) == 0 {
		return false, corev1.NodeCondition{}
	}

	// This block is not necessary. However, it makes the logic explicit: if a negative condition is not present, we ignore it.

	for _, condType := range r.ForbiddenConditionTypes {
		cond := conditions.GetNodeCondition(node.Status.Conditions, condType)
		if cond == nil {
			continue
		}
		if cond.Status == corev1.ConditionTrue {
			return true, corev1.NodeCondition{
				Type:               conditions.StringToConditionType(conditions.MaintenanceInProgressConditionType),
				Status:             conditions.BoolToConditionStatus(false),
				Reason:             conditions.MaintenanceInProgressConditionBadConditionsReason,
				Message:            fmt.Sprintf("forbidden condition %s is present on the node", condType),
				LastHeartbeatTime:  metav1.Now(),
				LastTransitionTime: metav1.Now(),
			}
		}
	}
	return false, corev1.NodeCondition{}
}

func (r *MaintenanceSchedulerNodeReconciler) checkRequiredConditions(node *corev1.Node) (bool, corev1.NodeCondition) {
	if len(r.RequiredConditionTypes) == 0 {
		return true, corev1.NodeCondition{}
	}

	for _, condType := range r.RequiredConditionTypes {
		cond := conditions.GetNodeCondition(node.Status.Conditions, condType)
		if cond == nil || cond.Status == corev1.ConditionFalse {
			return false, corev1.NodeCondition{
				Type:               conditions.StringToConditionType(conditions.MaintenanceInProgressConditionType),
				Status:             conditions.BoolToConditionStatus(false),
				Reason:             conditions.MaintenanceInProgressConditionBadConditionsReason,
				Message:            fmt.Sprintf("the required condition %s is not present on the node", condType),
				LastHeartbeatTime:  metav1.Now(),
				LastTransitionTime: metav1.Now(),
			}
		}
	}
	return true, corev1.NodeCondition{}
}

func (r *MaintenanceSchedulerNodeReconciler) checkConcurrencyLimit(ctx context.Context, node *corev1.Node) (bool, corev1.NodeCondition, error) {
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return false, corev1.NodeCondition{}, fmt.Errorf("failed to list nodes currently in maintenance for concurrency evaluation %w", err)
	}
	var nodesInMaintenance []string
	for _, n := range nodeList.Items {
		condition := conditions.GetNodeCondition(n.Status.Conditions, conditions.MaintenanceInProgressConditionType)
		if condition != nil && condition.Status == corev1.ConditionTrue {
			nodesInMaintenance = append(nodesInMaintenance, n.Name)
		}
	}
	r.Logger.Debug(fmt.Sprintf("%d nodes currently in maintenance", len(nodesInMaintenance)), "nodesInMaintenance", nodesInMaintenance)
	if len(nodesInMaintenance) >= r.MaximumNodesInMaintenance && !slices.Contains(nodesInMaintenance, node.Name) {
		return false, corev1.NodeCondition{
			Message:            fmt.Sprintf("%d nodes currently in maintenance reached", len(nodesInMaintenance)),
			Type:               conditions.StringToConditionType(conditions.MaintenanceInProgressConditionType),
			Status:             conditions.BoolToConditionStatus(false),
			Reason:             conditions.MaintenanceInProgressConditionTooManyNodesInMaintenanceReason,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
		}, nil
	}
	return true, corev1.NodeCondition{}, nil
}

func (r *MaintenanceSchedulerNodeReconciler) reconcileConditions(ctx context.Context, node *corev1.Node, conditionsToApply []corev1.NodeCondition) (ctrl.Result, error) {
	nodeCopy := node.DeepCopy()
	upToDate := true
	for _, conditionToApply := range conditionsToApply {
		if updated := conditions.UpsertNodeCondition(nodeCopy, conditionToApply, r.ConditionHeartbeatPeriod); updated {
			slog.Debug("Will update for new condition", "node", node.Name, "condition", conditionToApply)
			upToDate = false
		}
	}

	if !upToDate {
		patch := client.StrategicMergeFrom(node)
		if err := r.Status().Patch(ctx, nodeCopy, patch); err != nil {
			r.Logger.Error("failed to update node condition", "node", node.Name)
			return ctrl.Result{}, err
		}
		r.Logger.Debug("node condition updated", "node", node.Name)
	}
	return ctrl.Result{}, nil
}

type MaintenanceSchedulerCMReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	Logger              *slog.Logger
	cmChecksums         map[string]string
	ConfigMapNamespaces string
	ConfigMapPrefix     string
	ConfigMapLabelKey   string
	MaintenanceWindows  *Windows
}

// Reconcile is a big word. In this case, I simply want to reload the whole process, as this is very invasive.
// To clarify: Deleting a maintenance window is hard, especially because it contains a job which might be running.
// It would require rewiring the whole cron into this loop, and I have no energy to do this now.
func (r *MaintenanceSchedulerCMReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Logger.Info("Analysing matching config map")
	var cm corev1.ConfigMap
	if err := r.Get(ctx, req.NamespacedName, &cm); err != nil {
		if k8serrors.IsNotFound(err) {
			// If the custom resource is not found then it usually means that it was deleted
			// In this case, reload the whole code, so that it does a complete cleanup
			r.Logger.Warn("Rebooting controller due to change in configmap", "cmName", req.Name, "cmNamespace", req.Namespace)
			os.Exit(666)
		}
		return ctrl.Result{}, err
	}
	if reconciledWindow, err := NewWindowFromConfigMap(req.Name, cm.Data); err != nil || reconciledWindow == nil {
		return ctrl.Result{}, err
	} else {
		if knownWindow, ok := r.MaintenanceWindows.AllWindows[req.Name]; !ok || knownWindow.Checksum() != reconciledWindow.Checksum() {
			r.Logger.Warn("Rebooting controller due to the addition or edition of configmap", "cmName", req.Name, "cmNamespace", req.Namespace)
			os.Exit(666)
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MaintenanceSchedulerCMReconciler) SetupWithManager(mgr ctrl.Manager) error {
	match := func(obj client.Object) bool {
		if obj == nil {
			return false
		}
		// namespace filter (manager cache already can restrict namespaces)
		if r.ConfigMapNamespaces != "" && obj.GetNamespace() != r.ConfigMapNamespaces {
			return false
		}
		labels := obj.GetLabels()
		if labels != nil {
			if _, found := labels[r.ConfigMapLabelKey]; found {
				return true
			}
		}
		if r.ConfigMapPrefix != "" && strings.HasPrefix(obj.GetName(), r.ConfigMapPrefix) {
			return true
		}
		return false
	}
	predicates := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			cm, ok := e.Object.(*corev1.ConfigMap)
			if !ok || cm == nil {
				return false
			}
			return match(cm)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			cm, ok := e.ObjectNew.(*corev1.ConfigMap)
			if !ok || cm == nil {
				return false
			}
			return match(cm)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			cm, ok := e.Object.(*corev1.ConfigMap)
			if !ok || cm == nil {
				return false
			}
			return match(cm)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			cm, ok := e.Object.(*corev1.ConfigMap)
			if !ok || cm == nil {
				return false
			}
			return match(cm)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).For(&corev1.ConfigMap{}).WithEventFilter(predicates).Complete(r)
}
