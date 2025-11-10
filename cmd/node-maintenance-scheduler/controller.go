package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kubereboot/kured/internal/maintenances"
	"github.com/kubereboot/kured/pkg/conditions"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type MaintenanceQueues struct {
	mu      sync.Mutex
	pending []string
	active  []string
}

// Controller watches Node updates/deletes and processes them via a typed workQueue.
// It assigns nodes into maintenance queues based on their conditions, ignoring maintenance windows.
type Controller struct {
	logger             *slog.Logger
	client             kubernetes.Interface
	nodeLister         corev1listers.NodeLister
	workQueue          workqueue.TypedRateLimitingInterface[cache.ObjectName]
	informer           cache.SharedIndexInformer
	positiveConditions []string
	negativeConditions []string
	maintenanceQueues  *maintenances.Queues
}

// NewController creates a new instance of the Node controller, watching for node condition changes, regardless of maintenance windows.
func NewController(logger *slog.Logger, client kubernetes.Interface, nodeInformer corev1informers.NodeInformer, positiveConditions []string, negativeConditions []string, queues *maintenances.Queues) *Controller {
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
		// positiveConditions are conditions that, when present on a node, indicate it should be scheduled for maintenance
		positiveConditions: positiveConditions,
		// negativeConditions are conditions that, when present on a node, indicate it should NOT be scheduled for maintenance
		negativeConditions: negativeConditions,
		maintenanceQueues:  queues,
	}

	// Setting up event handlers for node changes to catch status updates (nodes enters or leaves a condition that is caught for maintenance reasons)
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		// No pre-filtering here. We accept all node events into workQueue. It workQueue will be larger, but it prevents the following risks:
		// - A new node has received a condition before the informer has synced, and we miss it
		// - A node has multiple conditions changing at once, and we miss one of them
		// - An update is relevant now but might not be when we handle the workQueue
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
		DeleteFunc: func(obj interface{}) {
			object := obj.(*corev1.Node)
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
		c.logger.Error("Error syncing node", "objRef", objRef, "err", err)
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
			c.maintenanceQueues.Dequeue(objectRef.Name)
			return nil
		}
		return fmt.Errorf("failed to get node %q: %w", objectRef, err)
	}

	if node == nil {
		return fmt.Errorf("node %q not found", objectRef.Name)
	}

	// Only process nodes that have positive conditions and do not have negative conditions
	// push node to pending queue if it matches
	needsMaintenance := conditions.Matches(node.Status.Conditions, c.positiveConditions, c.negativeConditions)
	if needsMaintenance {
		added := c.maintenanceQueues.Enqueue(node.Name)
		if added {
			c.logger.Debug("Node added to pending maintenance queue", "node", node.Name)
		}
	} else {
		removed := c.maintenanceQueues.Dequeue(node.Name)
		if removed {
			c.logger.Debug("Node removed from maintenance queues", "node", node.Name)
		}
	}
	return nil
}
