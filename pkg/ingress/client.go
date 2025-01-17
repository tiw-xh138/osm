package ingress

import (
	"reflect"

	networkingV1 "k8s.io/api/networking/v1"
	networkingV1beta1 "k8s.io/api/networking/v1beta1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/openservicemesh/osm/pkg/announcements"
	"github.com/openservicemesh/osm/pkg/configurator"
	k8s "github.com/openservicemesh/osm/pkg/kubernetes"
	"github.com/openservicemesh/osm/pkg/service"
)

// NewIngressClient implements ingress.Monitor and creates the Kubernetes client to monitor Ingress resources.
func NewIngressClient(kubeClient kubernetes.Interface, kubeController k8s.Controller, stop chan struct{}, cfg configurator.Configurator) (Monitor, error) {
	// TODO(#2798): Dynamically retrieve configured version
	// Currently, since only networking.k8s.io/v1beta1 is supported, hardcode this.
	requestedAPIVersion := IngressNetworkingV1beta1

	var informer cache.SharedIndexInformer
	informerFactory := informers.NewSharedInformerFactory(kubeClient, k8s.DefaultKubeEventResyncInterval)

	switch requestedAPIVersion {
	case IngressNetworkingV1:
		informer = informerFactory.Networking().V1().Ingresses().Informer()

	case IngressNetworkingV1beta1:
		informer = informerFactory.Networking().V1beta1().Ingresses().Informer()

	default:
		return nil, ErrUnsupportedAPIVersion
	}

	client := Client{
		informer:       informer,
		cache:          informer.GetStore(),
		cacheSynced:    make(chan interface{}),
		kubeController: kubeController,
		apiVersion:     requestedAPIVersion,
	}

	shouldObserve := func(obj interface{}) bool {
		ns := reflect.ValueOf(obj).Elem().FieldByName("ObjectMeta").FieldByName("Namespace").String()
		return kubeController.IsMonitoredNamespace(ns)
	}

	ingrEventTypes := k8s.EventTypes{
		Add:    announcements.IngressAdded,
		Update: announcements.IngressUpdated,
		Delete: announcements.IngressDeleted,
	}
	informer.AddEventHandler(k8s.GetKubernetesEventHandlers("Ingress", "Kubernetes", shouldObserve, ingrEventTypes))

	if err := client.run(stop); err != nil {
		log.Error().Err(err).Msg("Could not start Kubernetes Ingress client")
		return nil, err
	}

	return client, nil
}

// run executes informer collection.
func (c *Client) run(stop <-chan struct{}) error {
	log.Info().Msg("Ingress client started")

	if c.informer == nil {
		return errInitInformers
	}

	go c.informer.Run(stop)
	log.Info().Msgf("Waiting for Ingress informer cache sync")
	if !cache.WaitForCacheSync(stop, c.informer.HasSynced) {
		return errSyncingCaches
	}

	// Closing the cacheSynced channel signals to the rest of the system that... caches have been synced.
	close(c.cacheSynced)

	log.Info().Msgf("Cache sync finished for Ingress informer")
	return nil
}

// GetAPIVersion returns the ingress API version
func (c Client) GetAPIVersion() APIVersion {
	return c.apiVersion
}

// GetIngressNetworkingV1beta1 returns the networking.k8s.io/v1beta1 ingress resources whose backends correspond to the service
func (c Client) GetIngressNetworkingV1beta1(meshService service.MeshService) ([]*networkingV1beta1.Ingress, error) {
	if c.GetAPIVersion() != IngressNetworkingV1beta1 {
		return nil, errUnexpectedAPIVersion
	}

	var ingressResources []*networkingV1beta1.Ingress
	for _, ingressInterface := range c.cache.List() {
		ingress, ok := ingressInterface.(*networkingV1beta1.Ingress)
		if !ok {
			log.Error().Msg("Failed type assertion for Ingress in ingress cache")
			continue
		}

		// Extra safety - make sure we do not pay attention to Ingresses outside of observed namespaces
		if !c.kubeController.IsMonitoredNamespace(ingress.Namespace) {
			continue
		}

		// Check if the ingress resource belongs to the same namespace as the service
		if ingress.Namespace != meshService.Namespace {
			// The ingress resource does not belong to the namespace of the service
			continue
		}
		if backend := ingress.Spec.Backend; backend != nil && backend.ServiceName == meshService.Name {
			// Default backend service
			ingressResources = append(ingressResources, ingress)
			continue
		}

	ingressRule:
		for _, rule := range ingress.Spec.Rules {
			for _, path := range rule.HTTP.Paths {
				if path.Backend.ServiceName == meshService.Name {
					ingressResources = append(ingressResources, ingress)
					break ingressRule
				}
			}
		}
	}
	return ingressResources, nil
}

// GetIngressNetworkingV1 returns the networking.k8s.io/v1 ingress resources whose backends correspond to the service
func (c Client) GetIngressNetworkingV1(meshService service.MeshService) ([]*networkingV1.Ingress, error) {
	if c.GetAPIVersion() != IngressNetworkingV1 {
		return nil, errUnexpectedAPIVersion
	}

	var ingressResources []*networkingV1.Ingress
	for _, ingressInterface := range c.cache.List() {
		ingress, ok := ingressInterface.(*networkingV1.Ingress)
		if !ok {
			log.Error().Msg("Failed type assertion for Ingress in ingress cache")
			continue
		}

		// Extra safety - make sure we do not pay attention to Ingresses outside of observed namespaces
		if !c.kubeController.IsMonitoredNamespace(ingress.Namespace) {
			continue
		}

		// Check if the ingress resource belongs to the same namespace as the service
		if ingress.Namespace != meshService.Namespace {
			// The ingress resource does not belong to the namespace of the service
			continue
		}
		if backend := ingress.Spec.DefaultBackend; backend != nil && backend.Service.Name == meshService.Name {
			// Default backend service
			ingressResources = append(ingressResources, ingress)
			continue
		}

	ingressRule:
		for _, rule := range ingress.Spec.Rules {
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service.Name == meshService.Name {
					ingressResources = append(ingressResources, ingress)
					break ingressRule
				}
			}
		}
	}
	return ingressResources, nil
}
