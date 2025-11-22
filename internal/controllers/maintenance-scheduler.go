package controllers

import (
	"context"
	"fmt"

	"github.com/kubereboot/kured/internal/conditions"
	"github.com/kubereboot/kured/internal/maintenances"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	MaintenanceWindowControllerName = "maintenance-scheduler"
)

// MaintenanceSchedulerNodeReconciler reconciles a Node object for all the maintenances windows conditions
type MaintenanceSchedulerNodeReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	MaintenanceWindows *maintenances.Windows
}

// Reconcile update node conditions based on the maintenance windows
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (r *MaintenanceSchedulerNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	logger.Info("Reconciling Node for maintenance windows", "node", req.NamespacedName)
	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		if k8serrors.IsNotFound(err) {
			// If the custom resource is not found then it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	underMaintenance, mwName := r.MaintenanceWindows.MatchesAnyActiveSelector(node.Labels)

	// is there a way to avoid this?
	mc := conditions.ConvertFromNodeConditions(node.Status.Conditions)
	newCondition := metav1.Condition{
		Type:    conditions.UnderMaintenanceConditionType,
		Status:  map[bool]metav1.ConditionStatus{false: metav1.ConditionFalse, true: metav1.ConditionTrue}[underMaintenance],
		Reason:  conditions.UnderMaintenanceConditionReason,
		Message: fmt.Sprintf("Config map %s is putting node under maintenance", mwName),
	}

	changed := meta.SetStatusCondition(mc, newCondition)

	node.Status.Conditions = *conditions.ConvertToNodeConditions(*mc)

	if changed {
		if err := r.Status().Update(ctx, &node); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update node status: %w", err)
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MaintenanceSchedulerNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Named(MaintenanceWindowControllerName).
		Complete(r)
}
