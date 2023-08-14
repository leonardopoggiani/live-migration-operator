package apiserver

/*
Now we will create an API Server Manager that will create the K8S client and keep a reference to it.
It will also create a cache that will be used to create a cached K8S client,
initialize the cache properly and in the end handle the termination signals.
*/

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	defaultRetryPeriod = 2 * time.Second
)

// Options to customize Manager behaviour and pass information
type Options struct {
	Scheme         *runtime.Scheme
	Namespace      string
	Port           int
	AllowedDomains []string
}

type Manager interface {
	Start(stop <-chan struct{}) error
}

type manager struct {
	config          *rest.Config
	client          client.Client
	server          *apiServer
	started         bool
	internalStop    <-chan struct{}
	internalStopper chan<- struct{}
	cache           cache.Cache
	errSignal       *errSignaler
	port            int
	allowedDomains  []string
}

func (m *manager) Start(stop <-chan struct{}) error {
	defer close(m.internalStopper)
	// initialize this here so that we reset the signal channel state on every start
	m.errSignal = &errSignaler{errSignal: make(chan struct{})}
	m.waitForCache()

	srv, err := newApiServer(m.port, m.allowedDomains, m.client)
	if err != nil {
		return err
	}

	go func() {
		if err := srv.Start(m.internalStop); err != nil {
			m.errSignal.SignalError(err)
		}
	}()
	select {
	case <-stop:
		return nil
	case <-m.errSignal.GotError():
		// Error starting the cache
		return m.errSignal.Error()
	}
}

func (m *manager) waitForCache() {
	ctx := context.Background()
	if m.started {
		return
	}

	go func() {
		if err := m.cache.Start(ctx); err != nil {
			m.errSignal.SignalError(err)
		}
	}()

	// Wait for the caches to sync.
	m.cache.WaitForCacheSync(ctx)
	m.started = true
}
