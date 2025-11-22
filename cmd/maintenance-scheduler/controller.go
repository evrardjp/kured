package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/kubereboot/kured/internal/conditions"
	"github.com/kubereboot/kured/internal/maintenances"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// TODO: To move to initator as it will depend on the implementation at each company (for example, if nvidia sets a condition to a certain node you might want to use it to apply
var (
	requiredConditionTypes = []string{
		conditions.RebootRequiredConditionType,
		conditions.UnderMaintenanceConditionType,
	}
	forbiddenConditionTypes = []string{}
)

// Controller watches Node updates/deletes and processes them via a typed workQueue.
// It marks nodes as part of a maintenance window (or not).
type Controller struct {
	logger     *slog.Logger
	client     kubernetes.Interface
	nodeLister corev1listers.NodeLister
	workQueue  workqueue.TypedRateLimitingInterface[cache.ObjectName]
	informer   cache.SharedIndexInformer
	mw         *maintenances.Windows
	// these will only be used to move the node from in-maintenance state to actively maintained state, based on capacity
	maxNodesConcurrentlyMaintained int
	// conditionHeartbeatPeriod is the minimum time to spend between nodeConditions heartbeats. Prevents overloading the API server.
	conditionHeartbeatPeriod time.Duration
}

// NewController creates a new instance of the Node controller, watching for node condition changes, regardless of maintenance windows.
func NewController(logger *slog.Logger, client kubernetes.Interface, nodeInformer corev1informers.NodeInformer, mw *maintenances.Windows, maxNodesConcurrentlyMaintained int, heartbeat time.Duration) *Controller {
	// Modern typed rate limiter setup
	rateLimiter := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[cache.ObjectName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[cache.ObjectName]{Limiter: rate.NewLimiter(rate.Limit(50), 300)},
	)

	c := &Controller{
		logger:                         logger,
		client:                         client,
		nodeLister:                     nodeInformer.Lister(),
		workQueue:                      workqueue.NewTypedRateLimitingQueue(rateLimiter),
		informer:                       nodeInformer.Informer(),
		mw:                             mw,
		maxNodesConcurrentlyMaintained: maxNodesConcurrentlyMaintained,
		conditionHeartbeatPeriod:       heartbeat,
	}

	// Setting up event handlers for node changes to catch status updates (nodes enters or leaves a condition that is caught for maintenance reasons)
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		// No pre-filtering here. We accept all node events into workQueue. It workQueue will be larger, but it prevents the following risks:
		// - A new node has received a condition before the informer has synced, and we miss it
		// - A node has multiple conditions changing at once, and we miss one of them
		// - An update is relevant now but might not be when we handle the workQueue
		// - We ignore all delete events
		AddFunc: func(obj interface{}) {
			object := obj.(*corev1.Node)
			if object == nil {
				return
			}
			c.enqueueNode(object)
		},
		UpdateFunc: func(old, new interface{}) {
			object := new.(*corev1.Node)
			if object == nil {
				return
			}
			c.enqueueNode(object)
		},
	})
	return c
}

func (c *Controller) enqueueNode(obj interface{}) {
	if objectRef, err := cache.ObjectToName(obj); err != nil {
		utilruntime.HandleError(err)
		return
	} else {
		c.workQueue.Add(objectRef)
	}
}

// Run starts the controller workers until the context is canceled.
// Run will set up the event handlers for nodes, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shut down the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(ctx context.Context, workers int) error {
	defer c.workQueue.ShutDown()

	c.logger.Info("Starting Node controller")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	c.logger.Info("Starting workers", "workers", workers)
	for i := 0; i < workers; i++ {
		go c.runWorker(ctx)
	}

	<-ctx.Done()
	c.logger.Info("Shutting down Node controller")
	return nil
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextItem(ctx) {
	}
}

func (c *Controller) processNextItem(ctx context.Context) bool {
	objRef, shutdown := c.workQueue.Get()
	if shutdown {
		return false
	}
	defer c.workQueue.Done(objRef)

	if err := c.applyMaintenanceWindowCondition(ctx, objRef); err != nil {
		c.logger.Error("Error syncing node", "objRef", objRef, "error", err)
		c.workQueue.AddRateLimited(objRef)
		return true
	}

	if err := c.applyInMaintenanceCondition(ctx, objRef); err != nil {
		c.logger.Error("Error syncing node for start maintenance", "objRef", objRef, "error", err)
		c.workQueue.AddRateLimited(objRef)
		return true
	}

	c.workQueue.Forget(objRef)
	return true
}

// applyMaintenanceWindowCondition processes any node change and (un)sets its maintenance condition based on active maintenance windows.
func (c *Controller) applyMaintenanceWindowCondition(ctx context.Context, objectRef cache.ObjectName) error {
	//
	node, err := c.nodeLister.Get(objectRef.Name)

	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get node %q: %w", objectRef, err)
	}

	if node == nil {
		return fmt.Errorf("failed to get object %s from nodeLister", objectRef.Name)
	}

	underMaintenance, mwName := c.mw.MatchesAnyActiveSelector(node.Labels)
	currentCondition := corev1.NodeCondition{
		Type:               conditions.StringToConditionType(conditions.UnderMaintenanceConditionType),
		Status:             conditions.BoolToConditionStatus(underMaintenance),
		Reason:             conditions.UnderMaintenanceConditionReason,
		Message:            fmt.Sprintf("Config map %s is putting node under maintenance", mwName),
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
	}
	clientSet := c.client.(*kubernetes.Clientset)
	if err := conditions.UpdateNodeCondition(ctx, clientSet, node.ObjectMeta.Name, currentCondition, c.conditionHeartbeatPeriod); err != nil {
		return fmt.Errorf("failed to set %s condition %w", conditions.UnderMaintenanceConditionType, err)
	}
	return nil
}

// applyInMaintenanceCondition processes any node change by
// 1. checking whether the node matches ALL required conditions (e.g., be part of an active maintenance window, etc.) AND  NOT matching all blocking conditions (e.g., a future "DoNotRestart" condition)
// 2. checking whether the node has blocking labels/annotations
// 3. checking whether the node CAN start maintenance (e.g. below maximum concurrent nodes in maintenance)
// If all checks pass, the node is marked with the condition "kured.dev/maintenance-in-progress").
func (c *Controller) applyInMaintenanceCondition(ctx context.Context, objectRef cache.ObjectName) error {
	node, err := c.nodeLister.Get(objectRef.Name)

	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get node %q: %w", objectRef, err)
	}

	if node == nil {
		return fmt.Errorf("failed to get object %s from nodeLister", objectRef.Name)
	}

	// Happy path.
	inProgressCondition := corev1.NodeCondition{
		Type:               conditions.StringToConditionType(conditions.InProgressMaintenanceConditionType),
		Status:             conditions.BoolToConditionStatus(true),
		Reason:             conditions.InProgressMaintenanceConditionSuccessReason,
		Message:            "Everything required is present to start the node maintenance",
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
	}

	clientSet := c.client.(*kubernetes.Clientset)

	// First reason to put a false condition: Not all the criteria are matched
	if result, reason := conditions.Matches(node.Status.Conditions, requiredConditionTypes, forbiddenConditionTypes); !result {
		inProgressCondition.Status = conditions.BoolToConditionStatus(false)
		inProgressCondition.Reason = conditions.InProgressMaintenanceConditionBadConditionsReason
		if reason != nil {
			inProgressCondition.Message = fmt.Sprintf("condition %s prevents entering maintenance", reason.Type)
		} else {
			inProgressCondition.Message = "a required condition is not present on the node"
		}

		if err := conditions.UpdateNodeCondition(ctx, clientSet, node.ObjectMeta.Name, inProgressCondition, c.conditionHeartbeatPeriod); err != nil {
			return fmt.Errorf("failed to set %s condition %w", conditions.InProgressMaintenanceConditionType, err)
		}
	}
	// Todo: Add Label matchers (see https://github.com/kubereboot/kured/pull/1215)

	// Second reason to put a false condition: Too many nodes have the condition, cannot add a new one
	nodes, errListing := conditions.ListAllNodesWithConditionType(ctx, clientSet, conditions.InProgressMaintenanceConditionType)
	c.logger.Debug("nodes currently in maintenance", "count", len(nodes), "names", nodes)
	if errListing != nil {
		return fmt.Errorf("failed to list all nodes: %w", err)
	}
	if len(nodes) >= c.maxNodesConcurrentlyMaintained && !slices.Contains(nodes, node.ObjectMeta.Name) {
		inProgressCondition.Status = conditions.BoolToConditionStatus(false)
		inProgressCondition.Reason = conditions.InProgressMaintenanceConditionTooManyNodesInMaintenanceReason
		inProgressCondition.Message = fmt.Sprintf("cannot progress maintenance until other nodes have ended their maintenance")
		if err := conditions.UpdateNodeCondition(ctx, clientSet, node.ObjectMeta.Name, inProgressCondition, c.conditionHeartbeatPeriod); err != nil {
			return fmt.Errorf("failed to set %s condition %w", conditions.InProgressMaintenanceConditionType, err)
		}
	}

	if err := conditions.UpdateNodeCondition(ctx, clientSet, node.ObjectMeta.Name, inProgressCondition, c.conditionHeartbeatPeriod); err != nil {
		return fmt.Errorf("failed to set %s condition %w", conditions.InProgressMaintenanceConditionType, err)
	}
	return nil
}
