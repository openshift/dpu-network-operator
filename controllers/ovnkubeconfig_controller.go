/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/render"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dpuv1alpha1 "github.com/openshift/dpu-network-operator/api/v1alpha1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"

	syncer "github.com/openshift/dpu-network-operator/pkg/ovnkube-syncer"
	"github.com/openshift/dpu-network-operator/pkg/utils"
)

var logger = log.Log.WithName("controller_ovnkubeconfig")

// OVNKubeConfigReconciler reconciles a OVNKubeConfig object
type OVNKubeConfigReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	syncer           *syncer.OvnkubeSyncer
	stopCh           chan struct{}
	tenantRestConfig *rest.Config
}

//+kubebuilder:rbac:groups=dpu.openshift.io,resources=ovnkubeconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dpu.openshift.io,resources=ovnkubeconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dpu.openshift.io,resources=ovnkubeconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigpools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,resourceNames=anyuid,verbs=use

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OVNKubeConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *OVNKubeConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	logger := log.FromContext(ctx).WithValues("reconcile OVNKubeConfig", req.NamespacedName)
	logger.Info("Reconcile")
	ovnkubeConfig := &dpuv1alpha1.OVNKubeConfig{}

	cfgList := &dpuv1alpha1.OVNKubeConfigList{}
	err = r.List(ctx, cfgList, &client.ListOptions{Namespace: req.Namespace})
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(cfgList.Items) > 1 {
		logger.Error(fmt.Errorf("more than one OVNKubeConfig CR is found in"), "namespace", req.Namespace)
		return ctrl.Result{}, err
	} else if len(cfgList.Items) == 1 {
		ovnkubeConfig = &cfgList.Items[0]
		if ovnkubeConfig.Spec.KubeConfigFile == "" {
			logger.Info("kubeconfig of tenant cluster is not provided")
			// TODO: adding PF representor to br-ex
			return ctrl.Result{}, nil
		}
		if r.syncer == nil {
			logger.Info("Create the ovnkube syncer")
			r.stopCh = make(chan struct{})
			if err = r.startOvnSyncer(ctx, ovnkubeConfig); err != nil {
				return ctrl.Result{}, err
			}
		}
		if err = r.syncOvnkubeDaemonSet(ovnkubeConfig); err != nil {
			logger.Info("Sync DaemonSet ovnkube-node")
			return ctrl.Result{}, err
		}
	} else if len(cfgList.Items) == 0 {
		if r.syncer != nil {
			logger.Info("Stop the ovnkube syncer")
			close(r.stopCh)
			r.syncer = nil
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OVNKubeConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dpuv1alpha1.OVNKubeConfig{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}

func (r *OVNKubeConfigReconciler) startOvnSyncer(ctx context.Context, cfg *dpuv1alpha1.OVNKubeConfig) error {
	var err error
	s := &corev1.Secret{}

	err = r.Client.Get(ctx, types.NamespacedName{Name: cfg.Spec.KubeConfigFile, Namespace: cfg.Namespace}, s)
	if err != nil {
		return err
	}
	bytes, ok := s.Data["config"]
	if !ok {
		return fmt.Errorf("key 'config' cannot be found in secret %s", cfg.Spec.KubeConfigFile)
	}

	r.tenantRestConfig, err = clientcmd.RESTConfigFromKubeConfig(bytes)
	if err != nil {
		return err
	}

	r.syncer, err = syncer.New(syncer.SyncerConfig{
		// LocalClusterID:   cfg.Namespace,
		LocalRestConfig:  ctrl.GetConfigOrDie(),
		LocalNamespace:   cfg.Namespace,
		TenantRestConfig: r.tenantRestConfig,
		TenantNamespace:  utils.TenantNamespace}, cfg, r.Scheme)
	if err != nil {
		return err
	}
	go func() {
		if err = r.syncer.Start(r.stopCh); err != nil {
			logger.Error(err, "Error running the ovnkube syncer")
		}
	}()
	if err != nil {
		return err
	}

	return nil
}

func (r *OVNKubeConfigReconciler) syncOvnkubeDaemonSet(cfg *dpuv1alpha1.OVNKubeConfig) error {
	logger.Info("Start to sync ovnkube daemonset")
	var err error
	mcp := &mcfgv1.MachineConfigPool{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: cfg.Spec.PoolName}, mcp)
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("MachineConfigPool %s not found: %v", cfg.Spec.PoolName, err)
		}
	}

	image, err := r.getLocalOvnkubeImage()
	if err != nil {
		return err
	}

	data := render.MakeRenderData()
	data.Data["OvnKubeImage"] = image
	data.Data["OvnKubeImage"] = "quay.io/zshi/ovn-daemonset:arm-2042-20210629-f78a186"
	data.Data["Namespace"] = cfg.Namespace
	data.Data["TenantKubeconfig"] = cfg.Spec.KubeConfigFile

	objs := []*uns.Unstructured{}
	objs, err = render.RenderDir(utils.OvnkubeNodeManifestPath, &data)
	if err != nil {
		logger.Error(err, "Fail to render ovnkube-node daemon manifests")
		return err
	}
	// Sync DaemonSets
	for _, obj := range objs {
		switch obj.GetKind() {
		case "DaemonSet":
			scheme := scheme.Scheme
			ds := &appsv1.DaemonSet{}
			err = scheme.Convert(obj, ds, nil)
			if err != nil {
				logger.Error(err, "Fail to convert to DaemonSet")
				return err
			}
			ds.Spec.Template.Spec.NodeSelector = mcp.Spec.NodeSelector.MatchLabels
			err = scheme.Convert(ds, obj, nil)
			if err != nil {
				logger.Error(err, "Fail to convert to Unstructured")
				return err
			}
			if err := ctrl.SetControllerReference(cfg, obj, r.Scheme); err != nil {
				return err
			}
		default:
			if err := ctrl.SetControllerReference(cfg, obj, r.Scheme); err != nil {
				return err
			}
		}
		if err := apply.ApplyObject(context.TODO(), r.Client, obj); err != nil {
			return fmt.Errorf("failed to apply object %v with err: %v", obj, err)
		}
	}
	return nil
}

func (r *OVNKubeConfigReconciler) getLocalOvnkubeImage() (string, error) {
	ds := &appsv1.DaemonSet{}
	name := types.NamespacedName{Namespace: utils.LocalOvnkbueNamespace, Name: utils.LocalOvnkbueNodeDsName}
	err := r.Get(context.TODO(), name, ds)
	if err != nil {
		return "", err
	}
	return ds.Spec.Template.Spec.Containers[0].Image, nil
}
