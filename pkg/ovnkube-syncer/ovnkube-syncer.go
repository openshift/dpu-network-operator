package ovnkubesyncer

import (
	"fmt"
	"time"

	resourceSyncer "github.com/submariner-io/admiral/pkg/syncer"
	"github.com/submariner-io/admiral/pkg/syncer/broker"
	"github.com/submariner-io/admiral/pkg/util"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"

	dpuv1alpha1 "github.com/openshift/dpu-network-operator/api/v1alpha1"
	"github.com/openshift/dpu-network-operator/pkg/utils"
)

type SyncerConfig struct {
	// LocalRestConfig the REST config used to access the local resources to sync.
	LocalRestConfig *rest.Config

	// LocalClient the client used to access local resources to sync. This is optional and is provided for unit testing
	// in lieu of the LocalRestConfig. If not specified, one is created from the LocalRestConfig.
	LocalClient dynamic.Interface

	// LocalNamespace the namespace in the local source to which resources from the broker will be synced.
	LocalNamespace string

	// RestMapper used to obtain GroupVersionResources. This is optional and is provided for unit testing. If not specified,
	// one is created from the LocalRestConfig.
	RestMapper meta.RESTMapper

	// TenantRestConfig the REST config used to access the broker resources to sync.
	TenantRestConfig *rest.Config

	// TenantClient the client used to access local resources to sync. This is optional and is provided for unit testing
	// in lieu of the TenantRestConfig. If not specified, one is created from the TenantRestConfig.
	TenantClient dynamic.Interface

	// TenantNamespace the namespace in the broker to which resources from the local source will be synced.
	TenantNamespace string

	// Scheme used to convert resource objects. By default the global k8s Scheme is used.
	Scheme *runtime.Scheme
}

type OvnkubeSyncer struct {
	ConfigmapSyncer resourceSyncer.Interface
	SecretSyncer    resourceSyncer.Interface
	syncerConfig    SyncerConfig
	owner           *dpuv1alpha1.DpuClusterConfig
	scheme          *runtime.Scheme
}

func New(config SyncerConfig, owner *dpuv1alpha1.DpuClusterConfig, scheme *runtime.Scheme) (*OvnkubeSyncer, error) {
	var err error
	if config.RestMapper == nil {
		config.RestMapper, err = util.BuildRestMapper(config.LocalRestConfig)
		if err != nil {
			return nil, err
		}
	}

	if config.LocalClient == nil {
		config.LocalClient, err = dynamic.NewForConfig(config.LocalRestConfig)
		if err != nil {
			return nil, fmt.Errorf("error creating dynamic client: %v", err)
		}
	}

	if config.TenantClient == nil {
		config.TenantClient, err = dynamic.NewForConfig(config.TenantRestConfig)
		if err != nil {
			return nil, fmt.Errorf("error creating dynamic client: %v", err)
		}
	}
	syncer := &OvnkubeSyncer{
		syncerConfig: config,
		owner:        owner,
		scheme:       scheme,
	}

	return syncer, nil
}

func (s *OvnkubeSyncer) Start(stopCh <-chan struct{}) error {
	var err error
	klog.Info("Starting the ovnkube syncer")
	waitForCacheSync := true

	s.SecretSyncer, err = resourceSyncer.NewResourceSyncer(&resourceSyncer.ResourceSyncerConfig{
		Name:             "SecretSyncer",
		SourceClient:     s.syncerConfig.TenantClient,
		SourceNamespace:  s.syncerConfig.TenantNamespace,
		Direction:        resourceSyncer.None,
		RestMapper:       s.syncerConfig.RestMapper,
		Federator:        broker.NewFederator(s.syncerConfig.LocalClient, s.syncerConfig.RestMapper, s.syncerConfig.LocalNamespace, "", "ownerReferences"),
		ResourceType:     &corev1.Secret{},
		Transform:        s.shouldSyncSecret,
		WaitForCacheSync: &waitForCacheSync,
		Scheme:           s.scheme,
		ResyncPeriod:     5 * time.Second,
	})
	if err != nil {
		return err
	}

	s.ConfigmapSyncer, err = resourceSyncer.NewResourceSyncer(&resourceSyncer.ResourceSyncerConfig{
		Name:             "ConfigmapSyncer",
		SourceClient:     s.syncerConfig.TenantClient,
		SourceNamespace:  s.syncerConfig.TenantNamespace,
		Direction:        resourceSyncer.None,
		RestMapper:       s.syncerConfig.RestMapper,
		Federator:        broker.NewFederator(s.syncerConfig.LocalClient, s.syncerConfig.RestMapper, s.syncerConfig.LocalNamespace, "", "ownerReferences"),
		ResourceType:     &corev1.ConfigMap{},
		Transform:        s.shouldSyncConfigMap,
		WaitForCacheSync: &waitForCacheSync,
		Scheme:           s.scheme,
		ResyncPeriod:     5 * time.Second,
	})
	if err != nil {
		return err
	}

	klog.Info("Starting serviceaccount syncer")

	klog.Info("Starting secret syncer")
	err = s.SecretSyncer.Start(stopCh)
	if err != nil {
		return err
	}
	klog.Info("Starting configmap syncer")
	err = s.ConfigmapSyncer.Start(stopCh)
	if err != nil {
		return err
	}

	klog.Info("ovnkube syncer started")

	return nil
}

func (s *OvnkubeSyncer) shouldSyncSecret(obj runtime.Object, numRequeues int, op resourceSyncer.Operation) (runtime.Object, bool) {
	secret := obj.(*corev1.Secret)
	secret.Namespace = s.syncerConfig.LocalNamespace
	switch secret.Name {
	case utils.SecretNameOvnCert:
		// clear owner
		secret.OwnerReferences = []metav1.OwnerReference{}
		if err := ctrl.SetControllerReference(s.owner, secret, s.scheme); err != nil {
			return nil, false
		}
		return secret, false
	}
	return nil, false
}

func (s *OvnkubeSyncer) shouldSyncConfigMap(obj runtime.Object, numRequeues int, op resourceSyncer.Operation) (runtime.Object, bool) {
	cm := obj.(*corev1.ConfigMap)
	cm.Namespace = s.syncerConfig.LocalNamespace
	switch cm.Name {
	case utils.CmNameOvnCa, utils.CmNameOvnkubeConfig:
		// clear owner
		cm.OwnerReferences = []metav1.OwnerReference{}
		if err := ctrl.SetControllerReference(s.owner, cm, s.scheme); err != nil {
			return nil, false
		}
		return cm, false
	}
	return nil, false
}
