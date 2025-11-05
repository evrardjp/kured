package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// Controller watches Node updates/deletes and processes them via a workqueue.
type Controller struct {
	logger     *slog.Logger
	client     kubernetes.Interface
	nodeLister corev1listers.NodeLister
	queue      workqueue.RateLimitingInterface
	informer   cache.SharedIndexInformer
}

// NewController creates a new instance of the Node controller.
func NewController(logger *slog.Logger, client kubernetes.Interface, nodeInformer corev1informers.NodeInformer) *Controller {
	c := &Controller{
		logger:     logger,
		client:     client,
		nodeLister: nodeInformer.Lister(),
		queue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "nodes"),
		informer:   nodeInformer.Informer(),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			newNode := newObj.(*corev1.Node)
			key, err := cache.MetaNamespaceKeyFunc(newNode)
			if err == nil {
				c.queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if ok {
					node, _ = tombstone.Obj.(*corev1.Node)
				}
			}
			if node != nil {
				key, err := cache.MetaNamespaceKeyFunc(node)
				if err == nil {
					c.queue.Add(key)
				}
			}
		},
	})

	return c
}

// Run starts the controller workers until the context is cancelled.
func (c *Controller) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	c.logger.Info("Starting Node controller")

	if !cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced) {
		c.logger.Error("Failed to sync caches")
		return
	}

	for i := 0; i < workers; i++ {
		go c.runWorker(ctx)
	}

	<-ctx.Done()
	c.logger.Info("Shutting down Node controller")
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextItem(ctx) {
	}
}

func (c *Controller) processNextItem(ctx context.Context) bool {
	obj, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(obj)

	key, ok := obj.(string)
	if !ok {
		c.queue.Forget(obj)
		c.logger.Warn("Expected string in queue", "obj", obj)
		return true
	}

	if err := c.syncHandler(ctx, key); err != nil {
		c.logger.Error("Error syncing node", "key", key, "err", err)
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(obj)
	return true
}

func (c *Controller) syncHandler(ctx context.Context, key string) error {
	node, err := c.nodeLister.Get(key)
	if errors.IsNotFound(err) {
		c.handleDelete(ctx, node)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get node %q: %w", key, err)
	}
	c.handleUpdate(ctx, node)
	return nil
}

func (c *Controller) handleUpdate(ctx context.Context, node *corev1.Node) {
	c.logger.Info("Node update handled", "node", node.Name)
}

func (c *Controller) handleDelete(ctx context.Context, node *corev1.Node) {
	c.logger.Info("Node delete handled", "node", node.Name)
}
