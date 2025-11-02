package maintenances

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
)

var (
	UnderMaintenanceGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "kured",
		Name:      "under_maintenance",
		Help:      "nodes under maintenance",
	}, []string{"node"})
)

// Queue holds nodes in maintenance
// They are either waiting in "pending" slice or in "active" map.
// They should not be in both.
type Queue struct {
	Pending     []*corev1.Node          // nodes waiting to start maintenance
	Active      map[string]*corev1.Node // nodes currently in maintenance
	queueLength int
	mu          sync.Mutex
}

func NewQueue(length int) *Queue {
	return &Queue{
		queueLength: length,
	}
}

// If everything goes well, enqueue is done with a generic watch. This means that nodes will be enqueued based on when they change.
// It means the nodes that have been pending maintenance for the longest time will be processed first.

// Enqueue adds a node to the list of nodes in need of maintenance but waiting for their slot.
// Returns true if the node was added to the queue, false if already present in the queue.
func (q *Queue) Enqueue(node *corev1.Node) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, presentInActiveQueue := q.Active[node.Name]; presentInActiveQueue {
		return false
	}
	for _, curNode := range q.Pending {
		if curNode.Name == node.Name {
			return false
		}
	}
	q.Pending = append(q.Pending, node)
	return true
}

// Dequeue removes a node by its name from the list of nodes in need of maintenance.
// This is for nodes leaving maintenance (or deleted nodes)
// Returns the success of operation
func (q *Queue) Dequeue(nodeName string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	success := false
	for i, curNode := range q.Pending {
		if curNode.Name == nodeName {
			q.Pending = append(q.Pending[:i], q.Pending[i+1:]...)
			success = true
		}
	}
	if _, ok := q.Active[nodeName]; ok {
		delete(q.Active, nodeName)
		success = true
	}
	// I need to evaluate if it makes sense to decorrelate the gauge from the queue.
	UnderMaintenanceGauge.WithLabelValues(nodeName).Set(0)
	return success
}

// IsEmpty returns true if the queue is empty
//func (q *Queue) IsEmpty() bool {
//	q.mu.Lock()
//	defer q.mu.Unlock()
//	return len(q.Pending) == 0 && len(q.Active) == 0
//}

func (q *Queue) CanProcessActiveQueue() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.Active) < q.queueLength && len(q.Pending) > 0
}

// MaintainNode moves a node from pending to active.
// It returns true if the node was moved, false if not.
// It assumes CanProcessActiveQueue was called before and returned true.
// CAUTION: There is a possible race condition here.
// The returned node may be deleted by another goroutine before the caller can process it or if the node comes out of maintenance.
// This is not a problem is the caller is doing sanity checks on the node (existence and presence of condition)
// In that case, the caller can simply dequeue the node, which seems simple enough.
func (q *Queue) MaintainNode(nodeName string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	movedToActiveQueue := false
	if q.Active == nil {
		q.Active = make(map[string]*corev1.Node)
	}
	for i, curNode := range q.Pending {
		if curNode.Name == nodeName {
			movedToActiveQueue = true
			q.Pending = append(q.Pending[:i], q.Pending[i+1:]...)
			q.Active[nodeName] = curNode
			UnderMaintenanceGauge.WithLabelValues(nodeName).Set(1)
			break
		}
	}
	return movedToActiveQueue
}
