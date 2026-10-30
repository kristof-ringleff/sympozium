package harnessgateway

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Loader reads the profiles ConfigMap with a short TTL cache, so a new profile
// takes effect with no restart. Only this one ConfigMap in the Sympozium
// namespace counts: nothing an agent or a tenant writes can add a profile.
//
// It must be constructed with an UNCACHED reader, as pricing.Loader is.
type Loader struct {
	Reader    client.Reader
	Name      string
	Namespace string
	TTL       time.Duration
	// OnRejected is called once per load for each skipped entry.
	OnRejected func(string)

	mu        sync.Mutex
	set       *Set
	err       error
	fetchedAt time.Time
}

// Load returns the current profiles. Callers must fail closed on an error.
func (l *Loader) Load(ctx context.Context) (*Set, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ttl := l.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if !l.fetchedAt.IsZero() && time.Since(l.fetchedAt) < ttl {
		return l.set, l.err
	}

	var cm corev1.ConfigMap
	if err := l.Reader.Get(ctx, types.NamespacedName{Name: l.Name, Namespace: l.Namespace}, &cm); err != nil {
		l.set, l.err, l.fetchedAt = nil, err, time.Now()
		return nil, err
	}
	set, err := ParseSet([]byte(cm.Data[DataKey]))
	l.set, l.err, l.fetchedAt = set, err, time.Now()
	if err == nil && l.OnRejected != nil {
		for _, rejected := range set.Rejected {
			l.OnRejected(rejected)
		}
	}
	return set, err
}
