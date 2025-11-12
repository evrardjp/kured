package locks

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

var LeaseIndex int
var LockMutex sync.Mutex
var OwnsLease bool

type LeaseLock struct {
	GenericLock
	client      *kubernetes.Clientset
	nodeID      string
	namespace   string
	name        string
	concurrency int
}

func NewLeaseLock(client *kubernetes.Clientset, nodeID, namespace, name string, TTL time.Duration, concurrency int, lockReleaseDelay time.Duration) *LeaseLock {
	return &LeaseLock{
		GenericLock: GenericLock{
			TTL:          TTL,
			releaseDelay: lockReleaseDelay,
		},
		client:      client,
		nodeID:      nodeID,
		namespace:   namespace,
		name:        name,
		concurrency: concurrency,
	}
}

func (l *LeaseLock) Acquire() (bool, error) {
	// LeaseLock not implemented yet
	for i := 0; i < l.concurrency; i++ {
		ctx, _ := context.WithCancel(context.Background())

		lock := &resourcelock.LeaseLock{
			// configure LeaseLock
			Client: l.client.CoordinationV1(),
			LeaseMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%d", l.name, i),
				Namespace: l.namespace,
			},
			LockConfig: resourcelock.ResourceLockConfig{Identity: l.nodeID},
		}

		go leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			ReleaseOnCancel: true,
			RenewDeadline:   15 * time.Second,
			RetryPeriod:     5 * time.Second,
			LeaseDuration:   l.TTL,
			Lock:            lock,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(ctx context.Context) {
					LockMutex.Lock()
					defer LockMutex.Unlock()
					if OwnsLease {
						ctx.Done()
					}
					slog.Info("Acquired lock")
					OwnsLease = true
					LeaseIndex = i
					restartFunc()
				},
			},
		})
	}
	return nil
	return false, fmt.Errorf("lease lock not implemented yet")
}

func (l *LeaseLock) Release() error {
	return fmt.Errorf("lease lock not implemented yet")
}
