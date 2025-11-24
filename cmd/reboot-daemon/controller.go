package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/kubereboot/kured/internal/conditions"
	"github.com/kubereboot/kured/internal/labels"
	"github.com/kubereboot/kured/internal/reboot"
	"github.com/kubereboot/kured/internal/taints"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	kubectldrain "k8s.io/kubectl/pkg/drain"
)

// Controller watches Node updates/deletes and processes them via a typed workQueue.
// It marks nodes as part of a maintenance window (or not).
type Controller struct {
	logger     *slog.Logger
	client     kubernetes.Interface
	nodeLister corev1listers.NodeLister
	workQueue  workqueue.TypedRateLimitingInterface[cache.ObjectName]
	informer   cache.SharedIndexInformer
	// conditionHeartbeatPeriod is the minimum time to spend between nodeConditions heartbeats. Prevents overloading the API server.
	conditionHeartbeatPeriod time.Duration
	nodeName                 string
	rebooter                 reboot.Rebooter
	lastProcessedTime        time.Time
	processingMutex          sync.RWMutex
	drainDelay               time.Duration
	drainHelper              *kubectldrain.Helper
	preferNoScheduleTaint    *taints.Taint
}

// NewController creates a new instance of the Node controller, watching for node condition changes, regardless of maintenance windows.
func NewController(logger *slog.Logger, client kubernetes.Interface, nodeInformer corev1informers.NodeInformer, heartbeat time.Duration, nodeName string, rebooter reboot.Rebooter, drainDelay time.Duration, drainHelper *kubectldrain.Helper, preferNoScheduleTaint *taints.Taint) *Controller {
	// Modern typed rate limiter setup
	rateLimiter := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[cache.ObjectName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[cache.ObjectName]{Limiter: rate.NewLimiter(rate.Limit(50), 300)},
	)

	c := &Controller{
		logger:                   logger,
		client:                   client,
		nodeLister:               nodeInformer.Lister(),
		workQueue:                workqueue.NewTypedRateLimitingQueue(rateLimiter),
		informer:                 nodeInformer.Informer(),
		conditionHeartbeatPeriod: heartbeat,
		nodeName:                 nodeName,
		rebooter:                 rebooter,
		lastProcessedTime:        time.Now(),
		processingMutex:          sync.RWMutex{},
		drainDelay:               drainDelay,
		drainHelper:              drainHelper,
		preferNoScheduleTaint:    preferNoScheduleTaint,
	}

	// Setting up event handlers for node changes to catch status updates (nodes enters or leaves a condition that is caught for maintenance reasons)
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		// No pre-filtering here. We accept all node events into workQueue. It workQueue will be larger, but it prevents the following risks:
		// - A new node has received a condition before the informer has synced, and we miss it
		// - A node has multiple conditions changing at once, and we miss one of them
		// - An update is relevant now but might not be when we handle the workQueue
		// - We ignore all delete events
		AddFunc: func(obj interface{}) {
			newNode := obj.(*corev1.Node)
			if newNode == nil {
				return
			}
			if newNode.ObjectMeta.Name != nodeName {
				return
			}
			inProgressCondition := conditions.GetNodeCondition(newNode.Status.Conditions, conditions.MaintenanceInProgressConditionType)
			if inProgressCondition == nil {
				c.logger.Debug("not queuing node object (missing condition)")
				return
			}
			c.enqueueNode(newNode)
		},
		UpdateFunc: func(old, new interface{}) {
			oldNode := old.(*corev1.Node)
			newNode := new.(*corev1.Node)
			if newNode == nil {
				return
			}
			if newNode.ObjectMeta.Name != nodeName {
				return
			}
			oldNodeInProgressCondition := conditions.GetNodeCondition(oldNode.Status.Conditions, conditions.MaintenanceInProgressConditionType)
			newNodeInProgressCondition := conditions.GetNodeCondition(newNode.Status.Conditions, conditions.MaintenanceInProgressConditionType)

			if newNodeInProgressCondition == nil {
				c.logger.Debug("not queuing node object (missing condition)")
				return
			}

			if oldNodeInProgressCondition.Status != newNodeInProgressCondition.Status {
				c.logger.Debug("queuing node object (status changed)")
				c.enqueueNode(newNode)
				return
			}

			if time.Since(c.lastProcessedTime) > heartbeat {
				c.logger.Debug("queuing node object (periodic maintenance check)")
				c.enqueueNode(newNode)
				return
			}
			c.logger.Debug("not queuing node object (was already queued recently)")

			// This is a debug function alternative
			//func(old, new interface{}) {
			//oldNode := old.(*corev1.Node)
			//newNode := new.(*corev1.Node)
			//if newNode == nil {
			//	return
			//}
			//if newNode.ObjectMeta.Name != nodeName {
			//	return
			//}
			//oldNodeInProgressCondition := conditions.GetNodeCondition(oldNode.Status.Conditions, conditions.MaintenanceInProgressConditionType)
			//newNodeInProgressCondition := conditions.GetNodeCondition(newNode.Status.Conditions, conditions.MaintenanceInProgressConditionType)
			//if oldNodeInProgressCondition == nil || newNodeInProgressCondition == nil {
			//	c.logger.Debug("not queuing node object (missing condition)")
			//	return
			//}
			//if metav1.Now().Sub(newNodeInProgressCondition.LastHeartbeatTime.Time) <= heartbeat && oldNodeInProgressCondition.Status == newNodeInProgressCondition.Status {
			//	c.logger.Debug("not queuing node object (heartbeat recent, no status change)", "lastHeartbeatTime", newNodeInProgressCondition.LastHeartbeatTime.Time)
			//	return
			//}
			//
			//if oldNodeInProgressCondition.Status != newNodeInProgressCondition.Status {
			//	c.logger.Debug("queuing node object (status changed)")
			//	c.enqueueNode(newNode)
			//} else {
			//	c.logger.Debug("queuing node object (outdated heartbeat, no status change)", "lastHeartbeatTime", newNodeInProgressCondition.LastHeartbeatTime.Time)
			//	c.enqueueNode(newNode)
			//}
			//}

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

	c.logger.Info("Starting reboot-daemon controller")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	c.logger.Info("Starting workers", "workers", workers)
	for i := 0; i < workers; i++ {
		go c.runWorker(ctx)
	}

	<-ctx.Done()
	c.logger.Info("Shutting down reboot-daemon controller")
	return fmt.Errorf("shutting down reboot-daemon controller")
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

	c.processingMutex.Lock()
	c.lastProcessedTime = time.Now()
	c.processingMutex.Unlock()

	if err := c.rebootAsRequired(ctx, objRef); err != nil {
		c.logger.Error("error syncing node, queuing object again", "objRef", objRef, "error", err.Error())
		c.workQueue.AddRateLimited(objRef)
		return true
	}

	c.workQueue.Forget(objRef)
	return true
}

// rebootAsRequired processes any node changes and handles the reboot process.
func (c *Controller) rebootAsRequired(ctx context.Context, objectRef cache.ObjectName) error {
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

	inProgressCondition := conditions.GetNodeCondition(node.Status.Conditions, conditions.MaintenanceInProgressConditionType)
	if inProgressCondition == nil {
		c.logger.Debug("required condition is absent on the node %s", objectRef.Name)
		return nil
	}
	rebootDesired := inProgressCondition.Status == corev1.ConditionTrue
	c.logger.Debug("node condition info", "node", node.Name, "conditionType", conditions.MaintenanceInProgressConditionType, "conditionStatus", rebootDesired, "lastHeartbeatTime", inProgressCondition.LastHeartbeatTime.Time)

	clientSet := c.client.(*kubernetes.Clientset)

	c.logger.Info("Handling preferNoSchedule taint (if requested)", "node", c.nodeName, "desiredTaintState", map[bool]string{true: "present", false: "absent"}[rebootDesired])
	// Apply/Remove a taint as soon as we enter/leave maintenance
	c.preferNoScheduleTaint.SetState(rebootDesired)

	// Labels use case would generally come here. https://github.com/kubereboot/kured/issues/509
	// Yet it's not implemented ON PURPOSE. Why?
	// Because we do not need to fix the world problems anymore:
	// First, the WG node lifecycle is trying to standardize around conditions to inform maintenances
	// Second, even if we do not have a label set by this process, we can still set a label by another controller, based on conditions.
	// In both cases, there is 0 reason to do it here, as it can be done in a different channel.

	// Apply/Remove annotation before we Evict/after we return the node as active (removed the taint)
	previouslyUnschedulable := node.Spec.Unschedulable
	if previouslyUnschedulableAnnotation, ok := node.Annotations[labels.KuredNodeWasUnschedulableBeforeDrainAnnotation]; !ok {
		// No annotation might be fine. Do we need to reboot? Then we need to save Unschedulable spec in annotation.
		if rebootDesired {
			annotations := map[string]string{labels.KuredNodeWasUnschedulableBeforeDrainAnnotation: strconv.FormatBool(node.Spec.Unschedulable)}
			c.logger.Info(fmt.Sprintf("adding annotation %s", labels.KuredNodeWasUnschedulableBeforeDrainAnnotation), "node", c.nodeName)
			err := labels.AddNodeAnnotations(clientSet, c.nodeName, annotations)
			if err != nil {
				return fmt.Errorf("error saving state of the node %s, %v", c.nodeName, err)
			}
		}
	} else {
		// Annotation is present. Restore its content into memory.
		// Never update it here. If an error occurred later in the process, we always want to read the declared user state.
		previouslyUnschedulable, err = strconv.ParseBool(previouslyUnschedulableAnnotation)
		if err != nil {
			// It's worth not continuing, we do not know if we need to uncordon or not.
			// We can resume later or after the user has fixed the annotation. Until then, don't touch.
			return fmt.Errorf("error recovering state of the node %s, %v", c.nodeName, err)
		}
	}
	// if rebootDesired, then cordon (regardless of previous state)
	// if reboot not desired anymore, then uncordon UNLESS the previous state was already cordonned/unschedulable
	if rebootDesired || !previouslyUnschedulable {
		if errCordon := kubectldrain.RunCordonOrUncordon(c.drainHelper, node, rebootDesired); errCordon != nil {
			return fmt.Errorf("cordonning node %s failed: %w", c.nodeName, errCordon)
		}
	}

	if rebootDesired {
		c.logger.Info("Draining node", "node", c.nodeName)
		if errDrain := kubectldrain.RunNodeDrain(c.drainHelper, c.nodeName); errDrain != nil {
			return fmt.Errorf("error draining node %s: %v", c.nodeName, errDrain)
		}
	} else {
		c.logger.Info("Ensuring absent maintenance annotation", "node", c.nodeName)
		if errDelAnnotation := labels.DeleteNodeAnnotation(clientSet, c.nodeName, labels.KuredNodeWasUnschedulableBeforeDrainAnnotation); errDelAnnotation != nil {
			return fmt.Errorf("error cleaning annotation containing previous Unschedulable state of the node %s, %v", c.nodeName, errDelAnnotation)
		}
	}

	if rebootDesired {
		c.logger.Info("Rebooting node", "node", c.nodeName)
		if errR := c.rebooter.Reboot(); errR != nil {
			return fmt.Errorf("error rebooting node %s: %w", c.nodeName, errR)
		}
	}
	return nil
}
