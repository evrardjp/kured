package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kubereboot/kured/internal/maintenances"
	"github.com/kubereboot/kured/pkg/conditions"
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

// Controller watches Node updates/deletes and processes them via a typed workQueue.
// It assigns nodes into maintenance queues based on their conditions, ignoring maintenance windows.
type Controller struct {
	logger     *slog.Logger
	client     kubernetes.Interface
	nodeLister corev1listers.NodeLister
	workQueue  workqueue.TypedRateLimitingInterface[cache.ObjectName]
	informer   cache.SharedIndexInformer
	mw         *maintenances.Windows
}

// NewController creates a new instance of the Node controller, watching for node condition changes, regardless of maintenance windows.
func NewController(logger *slog.Logger, client kubernetes.Interface, nodeInformer corev1informers.NodeInformer, mw *maintenances.Windows) *Controller {
	// Modern typed rate limiter setup
	rateLimiter := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[cache.ObjectName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[cache.ObjectName]{Limiter: rate.NewLimiter(rate.Limit(50), 300)},
	)

	c := &Controller{
		logger:     logger,
		client:     client,
		nodeLister: nodeInformer.Lister(),
		workQueue:  workqueue.NewTypedRateLimitingQueue(rateLimiter),
		informer:   nodeInformer.Informer(),
		mw:         mw,
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

	if err := c.syncHandler(ctx, objRef); err != nil {
		c.logger.Error("Error syncing node", "objRef", objRef, "error", err)
		c.workQueue.AddRateLimited(objRef)
		return true
	}

	c.workQueue.Forget(objRef)
	return true
}

// syncHandler processes a node regardless of its maintenance window status.
// It checks if the node meets the criteria for maintenance based on its conditions.
// If the node requires maintenance, it is added to the pending maintenance queue.
// If it does not require maintenance, it is removed from all maintenance queues.
func (c *Controller) syncHandler(ctx context.Context, objectRef cache.ObjectName) error {
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

	if underMaintenance, mwName := c.mw.MatchesAnyActiveSelector(node.Labels); underMaintenance {
		currentCondition := corev1.NodeCondition{
			Type:               conditions.StringToConditionType(conditions.UnderMaintenanceConditionType),
			Status:             conditions.BoolToConditionStatus(true),
			Reason:             "KuredHasMaintenanceWindowConfigMap",
			Message:            fmt.Sprintf("Config map %s is putting node under maintenance", mwName),
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
		}
		clientSet := c.client.(*kubernetes.Clientset)

		if err := conditions.UpdateNodeCondition(ctx, clientSet, node.ObjectMeta.Name, currentCondition); err != nil {
			return fmt.Errorf("failed to set UnderMaintenance condition %w", err)
		}
	}
	return nil
}
