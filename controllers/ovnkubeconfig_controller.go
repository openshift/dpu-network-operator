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
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/render"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcrender "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/render"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"

	dpuv1alpha1 "github.com/openshift/dpu-network-operator/api/v1alpha1"
	syncer "github.com/openshift/dpu-network-operator/pkg/ovnkube-syncer"
	"github.com/openshift/dpu-network-operator/pkg/utils"
)

const (
	dpuMcRole = "dpu-worker"
)

var logger = log.Log.WithName("controller_ovnkubeconfig")

const (
	OVN_NB_PORT = "9641"
	OVN_SB_PORT = "9642"
)

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
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigs,verbs=get;list;watch;create;update;patch;delete
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
		if ovnkubeConfig.Spec.PoolName == "" {
			logger.Info("poolName is not provided")
			return ctrl.Result{}, nil
		} else {
			err = r.syncMachineConfigObjs(ovnkubeConfig.Spec)
			if err != nil {
				return ctrl.Result{}, err
			}
		}

		if ovnkubeConfig.Spec.KubeConfigFile == "" {
			logger.Info("kubeconfig of tenant cluster is not provided")
			return ctrl.Result{}, nil
		}
		if r.syncer == nil {
			logger.Info("Create the ovnkube syncer")
			r.stopCh = make(chan struct{})
			if err = r.startOvnSyncer(ctx, ovnkubeConfig); err != nil {
				return ctrl.Result{}, err
			}
		}
		if err = r.syncOvnkubeDaemonSet(ctx, ovnkubeConfig); err != nil {
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

func (r *OVNKubeConfigReconciler) syncOvnkubeDaemonSet(ctx context.Context, cfg *dpuv1alpha1.OVNKubeConfig) error {
	logger.Info("Start to sync ovnkube daemonset")
	var err error
	mcp := &mcfgv1.MachineConfigPool{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: cfg.Spec.PoolName}, mcp)
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("MachineConfigPool %s not found: %v", cfg.Spec.PoolName, err)
		}
	}

	masterIPs, err := r.getTenantClusterMasterIPs(ctx)
	if err != nil {
		logger.Error(err, "failed to get the ovnkube master IPs")
		return nil
	}

	image := os.Getenv("OVNKUBE_IMAGE")
	if image == "" {
		image, err = r.getLocalOvnkubeImage()
		if err != nil {
			return err
		}
	}

	data := render.MakeRenderData()
	data.Data["OvnKubeImage"] = image
	data.Data["Namespace"] = cfg.Namespace
	data.Data["TenantKubeconfig"] = cfg.Spec.KubeConfigFile
	data.Data["OVN_NB_DB_LIST"] = dbList(masterIPs, OVN_NB_PORT)
	data.Data["OVN_SB_DB_LIST"] = dbList(masterIPs, OVN_SB_PORT)

	objs, err := render.RenderDir(utils.OvnkubeNodeManifestPath, &data)
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
			for k, v := range mcp.Spec.NodeSelector.MatchLabels {
				ds.Spec.Template.Spec.NodeSelector[k] = v
			}
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

func (r *OVNKubeConfigReconciler) syncMachineConfigObjs(cs dpuv1alpha1.OVNKubeConfigSpec) error {
	var err error
	foundMc := &mcfgv1.MachineConfig{}
	foundMcp := &mcfgv1.MachineConfigPool{}
	mcp := &mcfgv1.MachineConfigPool{}
	mcp.Name = cs.PoolName
	mcSelector, err := metav1.ParseToLabelSelector(fmt.Sprintf("%s in (worker,%s)", mcfgv1.MachineConfigRoleLabelKey, dpuMcRole))
	if err != nil {
		return err
	}
	mcp.Spec = mcfgv1.MachineConfigPoolSpec{
		MachineConfigSelector: mcSelector,
		NodeSelector:          cs.NodeSelector,
	}
	if cs.PoolName == "master" || cs.PoolName == "worker" {
		return fmt.Errorf("%s pools is not allowed", cs.PoolName)
	}

	err = r.Get(context.TODO(), types.NamespacedName{Name: cs.PoolName}, foundMcp)
	if err != nil {
		if errors.IsNotFound(err) {

			err = r.Create(context.TODO(), mcp)
			if err != nil {
				return fmt.Errorf("couldn't create MachineConfigPool: %v", err)
			}
			logger.Info("Created MachineConfigPool:", "name", cs.PoolName)
		}
	} else {
		if !(equality.Semantic.DeepEqual(foundMcp.Spec.MachineConfigSelector, mcSelector) && equality.Semantic.DeepEqual(foundMcp.Spec.NodeSelector, cs.NodeSelector)) {
			logger.Info("MachineConfigPool already exists, updating")
			foundMcp.Spec = mcp.Spec
			err = r.Update(context.TODO(), foundMcp)
			if err != nil {
				return fmt.Errorf("couldn't update MachineConfigPool: %v", err)
			}
		} else {
			logger.Info("No content change, skip updating MCP")
		}
	}

	mcName := "00-" + cs.PoolName + "-" + "bluefield-switchdev"

	data := mcrender.MakeRenderData()
	pfRepName := os.Getenv("PF_REP_NAME")
	if pfRepName == "" {
		// the default name of the PF representor
		pfRepName = "pf0hpf"
	}
	data.Data["PfRepName"] = pfRepName
	mc, err := mcrender.GenerateMachineConfig("bindata/machine-config", mcName, dpuMcRole, true, &data)
	if err != nil {
		return err
	}

	err = r.Get(context.TODO(), types.NamespacedName{Name: mcName}, foundMc)
	if err != nil {
		if errors.IsNotFound(err) {
			err = r.Create(context.TODO(), mc)
			if err != nil {
				return fmt.Errorf("couldn't create MachineConfig: %v", err)
			}
			logger.Info("Created MachineConfig CR in MachineConfigPool", mcName, cs.PoolName)
		} else {
			return fmt.Errorf("failed to get MachineConfig: %v", err)
		}
	} else {
		if !bytes.Equal(foundMc.Spec.Config.Raw, mc.Spec.Config.Raw) {
			logger.Info("MachineConfig already exists, updating")
			foundMc.Spec.Config.Raw = mc.Spec.Config.Raw
			err = r.Update(context.TODO(), foundMc)
			if err != nil {
				return fmt.Errorf("couldn't update MachineConfig: %v", err)
			}
		} else {
			logger.Info("No content change, skip updating MC")
		}
	}
	return nil
}

func (r *OVNKubeConfigReconciler) getTenantClusterMasterIPs(ctx context.Context) ([]string, error) {
	c, err := client.New(r.tenantRestConfig, client.Options{})
	if err != nil {
		logger.Error(err, "Fail to create client for the tenant cluster")
		return []string{}, err
	}
	ovnkubeMasterPods := corev1.PodList{}
	labelSelector := labels.SelectorFromSet(map[string]string{"app": "ovnkube-master"})
	listOps := &client.ListOptions{LabelSelector: labelSelector}
	err = c.List(ctx, &ovnkubeMasterPods, listOps)
	if err != nil {
		logger.Error(err, "Fail to get the ovnkube-master pods of the tenant cluster")
		return []string{}, err
	}
	masterIPs := []string{}
	for _, pod := range ovnkubeMasterPods.Items {
		masterIPs = append(masterIPs, pod.Status.PodIP)
	}
	return masterIPs, nil
}

func dbList(masterIPs []string, port string) string {
	addrs := make([]string, len(masterIPs))
	for i, ip := range masterIPs {
		addrs[i] = "ssl:" + net.JoinHostPort(ip, port)
	}
	return strings.Join(addrs, ",")
}
