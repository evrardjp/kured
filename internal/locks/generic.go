package locks

import (
	"time"

	"k8s.io/client-go/kubernetes"
)

// Lock defines the interface for acquiring, releasing, and checking
// the status of a reboot coordination lock.
type Lock interface {
	Acquire() (bool, error)
	Release() error
}

// GenericLock holds the configuration for lock TTL and the delay before releasing it.
type GenericLock struct {
	TTL          time.Duration
	releaseDelay time.Duration
}

// New creates a daemonsetLock object containing the necessary data for follow-up k8s requests
func New(client *kubernetes.Clientset, nodeID, namespace, name, annotation, lockType string, TTL time.Duration, concurrency int, lockReleaseDelay time.Duration) Lock {
	if lockType == "lease" {
		return NewLeaseLock(client, nodeID, namespace, name, TTL, concurrency, lockReleaseDelay)
	}
	if concurrency > 1 {
		return &DaemonSetMultiLock{
			GenericLock: GenericLock{
				TTL:          TTL,
				releaseDelay: lockReleaseDelay,
			},
			DaemonSetLock: DaemonSetLock{
				client:     client,
				nodeID:     nodeID,
				namespace:  namespace,
				name:       name,
				annotation: annotation,
			},
			maxOwners: concurrency,
		}
	}
	return &DaemonSetSingleLock{
		GenericLock: GenericLock{
			TTL:          TTL,
			releaseDelay: lockReleaseDelay,
		},
		DaemonSetLock: DaemonSetLock{
			client:     client,
			nodeID:     nodeID,
			namespace:  namespace,
			name:       name,
			annotation: annotation,
		},
	}
}
