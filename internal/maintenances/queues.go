package maintenances

import (
	"sync"
)

// Queues holds two kinds of queues for node maintenance management.
// One is a "pending maintenance" slice, while the other is the "active maintenance" map.
// A node should never be in both.
type Queues struct {
	pending []string            // nodes waiting to start maintenance
	active  map[string]struct{} // nodes currently in maintenance
	size    int
	mu      sync.Mutex
}

// NewQueues creates a new Queues instance with the specified active queue size.
// Important to use this, as we don't check later for nil maps.
func NewQueues(length int) *Queues {
	return &Queues{
		size:   length,
		active: make(map[string]struct{}, length),
	}
}

// Enqueue adds a node to the list of nodes in need of maintenance but waiting for their slot.
// Returns true if the node was added to the queue, false if already present in the queue.
func (q *Queues) Enqueue(nodeName string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, presentInActiveQueue := q.active[nodeName]; presentInActiveQueue {
		return false
	}
	for _, curNode := range q.pending {
		if curNode == nodeName {
			return false
		}
	}
	q.pending = append(q.pending, nodeName)
	return true
}

// Dequeue removes a node by its name from the list of nodes in need of maintenance.
// This is for nodes leaving maintenance (or deleted nodes)
// Returns the success of operation
// Note: If the node did not exist in either queue, it returns false.
func (q *Queues) Dequeue(nodeName string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	success := false
	for i, curNode := range q.pending {
		if curNode == nodeName {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			success = true
		}
	}
	if _, ok := q.active[nodeName]; ok {
		delete(q.active, nodeName)
		success = true
	}
	return success
}

// IsEmpty returns true if the queue is empty
//func (q *Queue) IsEmpty() bool {
//	q.mu.Lock()
//	defer q.mu.Unlock()
//	return len(q.pending) == 0 && len(q.active) == 0
//}

func (q *Queues) CanProcessActiveQueue() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.active) < q.size && len(q.pending) > 0
}

// ProcessNode moves a node from pending to active if necessary and returns the presence in active.
// CAUTION: There is a possible race condition here.
// Another goroutine might have deleted the node given in the argument.
// This is not a problem is the caller is doing sanity checks on the node (existence and presence of condition)
// In that case, the caller can simply dequeue the node, which seems simple enough.
func (q *Queues) ProcessNode(nodeName string) bool {
	if _, presentInActiveQueue := q.active[nodeName]; presentInActiveQueue {
		return true
	}
	if !q.CanProcessActiveQueue() {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, curNode := range q.pending {
		if curNode == nodeName {
			q.active[nodeName] = struct{}{}
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			return true
		}
	}
	return false
}
