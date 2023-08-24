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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/render"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcrender "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/render"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"

	"github.com/openshift/dpu-network-operator/api"
	dpuv1alpha1 "github.com/openshift/dpu-network-operator/api/v1alpha1"
	syncer "github.com/openshift/dpu-network-operator/pkg/ovnkube-syncer"
	"github.com/openshift/dpu-network-operator/pkg/utils"
)

const (
	dpuMcRole                    = "dpu-worker"
	OVN_MASTER_DISCOVERY_POLL    = 5
	OVN_MASTER_DISCOVERY_BACKOFF = 120
	OVN_NB_PORT                  = "9641"
	OVN_SB_PORT                  = "9642"
)

var (
	logger                       = log.Log.WithName("controller_dpuclusterconfig")
	ovn_master_discovery_timeout = 250
)

// DpuClusterConfigReconciler reconciles a DpuClusterConfig object
type DpuClusterConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	syncer *syncer.OvnkubeSyncer
	stopCh chan struct{}
}

type nodeInfo struct {
	address string
	created time.Time
}

type nodeInfoList []nodeInfo

func (l nodeInfoList) Len() int {
	return len(l)
}

func (l nodeInfoList) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

func (l nodeInfoList) Less(i, j int) bool {
	return l[i].created.Before(l[j].created)
}

//+kubebuilder:rbac:groups=dpu.openshift.io,resources=dpuclusterconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dpu.openshift.io,resources=dpuclusterconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dpu.openshift.io,resources=dpuclusterconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigpools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,resourceNames=anyuid;hostnetwork,verbs=use

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the DpuClusterConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *DpuClusterConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	logger := log.FromContext(ctx).WithValues("reconcile DpuClusterConfig", req.NamespacedName)
	logger.Info("Reconcile")
	dpuClusterConfig := &dpuv1alpha1.DpuClusterConfig{}

	cfgList := &dpuv1alpha1.DpuClusterConfigList{}
	err = r.List(ctx, cfgList, &client.ListOptions{Namespace: req.Namespace})
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(cfgList.Items) > 1 {
		logger.Error(fmt.Errorf("more than one DpuClusterConfig CR is found in"), "namespace", req.Namespace)
		return ctrl.Result{}, err
	} else if len(cfgList.Items) == 1 {
		dpuClusterConfig = &cfgList.Items[0]

		defer func() {
			if err := r.Status().Update(context.TODO(), dpuClusterConfig); err != nil {
				logger.Error(err, "unable to update DpuClusterConfig status")
			}
		}()

		if dpuClusterConfig.Spec.PoolName == "" {
			logger.Info("poolName is not provided")
			return ctrl.Result{}, nil
		} else {
			err = r.syncMachineConfigObjs(dpuClusterConfig.Spec)
			if err != nil {
				meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().NotMcpReady().Reason(api.ReasonFailedCreated).Msg(err.Error()).Build())
				return ctrl.Result{}, err
			}
			meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().McpReady().Reason(api.ReasonCreated).Build())
		}

		if dpuClusterConfig.Spec.KubeConfigFile == "" {
			logger.Info("kubeconfig of tenant cluster is not provided")
			return ctrl.Result{}, nil
		}
		if r.syncer == nil {
			logger.Info("Create the tenant syncer")
			r.stopCh = make(chan struct{})
			if err = r.startTenantSyncer(ctx, dpuClusterConfig); err != nil {
				meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().NotTenantObjsSynced().Reason(api.ReasonFailedStart).Msg(err.Error()).Build())
				return ctrl.Result{}, err
			}
			if err := r.isTenantObjsSynced(ctx, req.Namespace); err != nil {
				meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().NotTenantObjsSynced().Reason(api.ReasonNotFound).Msg(err.Error()).Build())
			} else {
				meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().TenantObjsSynced().Reason(api.ReasonCreated).Build())
			}
		}
		if err = r.syncOvnkubeDaemonSet(ctx, dpuClusterConfig); err != nil {
			logger.Info("Sync DaemonSet ovnkube-node")
			meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().NotOvnKubeReady().Reason(api.ReasonFailedCreated).Msg(err.Error()).Build())
			return ctrl.Result{}, err
		}
		ds := appsv1.DaemonSet{}
		if err = r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: "ovnkube-node"}, &ds); err != nil {
			meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().NotOvnKubeReady().Reason(api.ReasonNotFound).Msg(err.Error()).Build())
			return ctrl.Result{}, err
		}
		if ds.Status.DesiredNumberScheduled == ds.Status.NumberReady {
			meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().OvnKubeReady().Reason(api.ReasonCreated).Build())
		} else {
			meta.SetStatusCondition(&dpuClusterConfig.Status.Conditions, *api.Conditions().NotOvnKubeReady().Reason(api.ReasonProgressing).Msg("DaemonSet 'ovnkube-node' is rolling out").Build())
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
func (r *DpuClusterConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dpuv1alpha1.DpuClusterConfig{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}

func (r *DpuClusterConfigReconciler) startTenantSyncer(ctx context.Context, cfg *dpuv1alpha1.DpuClusterConfig) error {
	logger.Info("Start the tenant syncer")
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

	utils.TenantRestConfig, err = clientcmd.RESTConfigFromKubeConfig(bytes)
	if err != nil {
		return err
	}

	r.syncer, err = syncer.New(syncer.SyncerConfig{
		// LocalClusterID:   cfg.Namespace,
		LocalRestConfig:  ctrl.GetConfigOrDie(),
		LocalNamespace:   cfg.Namespace,
		TenantRestConfig: utils.TenantRestConfig,
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

func (r *DpuClusterConfigReconciler) syncOvnkubeDaemonSet(ctx context.Context, cfg *dpuv1alpha1.DpuClusterConfig) error {
	logger.Info("Start to sync ovnkube daemonset")
	var err error
	mcp := &mcfgv1.MachineConfigPool{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: cfg.Spec.PoolName}, mcp)
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("MachineConfigPool %s not found: %v", cfg.Spec.PoolName, err)
		}
	}

	masterIPs, newTimeout, err := r.getTenantClusterMasterIPs(ovn_master_discovery_timeout)
	if err != nil {
		logger.Error(err, "failed to get master node IPs")
		return nil
	}
	ovn_master_discovery_timeout = newTimeout

	image := os.Getenv("OVNKUBE_IMAGE")
	if image == "" {
		image, err = r.getLocalOvnkubeImage()
		if err != nil {
			return err
		}
	}

	data := render.MakeRenderData()

	// TODO: KUBE_RBAC_PROXY_IMAGE should be specified when running the operator...
	// in CNO it's defined in the YAML for CNO itself:
	// - name: KUBE_RBAC_PROXY_IMAGE
	//   value: "quay.io/openshift/origin-kube-rbac-proxy:latest"
	// https://github.com/openshift/cluster-network-operator/blob/master/manifests/0000_70_cluster-network-operator_03_deployment.yaml#L69-L70
	data.Data["KubeRBACProxyImage"] = "quay.io/openshift/origin-kube-rbac-proxy:latest" // os.Getenv("KUBE_RBAC_PROXY_IMAGE")

	data.Data["OvnKubeImage"] = image
	data.Data["Namespace"] = cfg.Namespace
	data.Data["TenantKubeconfig"] = cfg.Spec.KubeConfigFile
	data.Data["OVN_NB_PORT"] = OVN_NB_PORT
	data.Data["OVN_SB_PORT"] = OVN_SB_PORT
	data.Data["LISTEN_DUAL_STACK"] = listenDualStack(masterIPs[0])

	if len(masterIPs) == 1 {
		data.Data["NorthdThreads"] = 1
	} else {
		// OVN 22.06 and later support multiple northd threads.
		// Less resource constrained clusters can use multiple threads
		// in northd to improve network operation latency at the cost
		// of a bit of CPU.
		data.Data["NorthdThreads"] = 4
	}
	data.Data["OVN_LOG_PATTERN_CONSOLE"] = "%D{%Y-%m-%dT%H:%M:%S.###Z}|%05N|%c%T|%p|%m"
	data.Data["OVN_NORTHD_PROBE_INTERVAL"] = 10000

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

func (r *DpuClusterConfigReconciler) getLocalOvnkubeImage() (string, error) {
	ds := &appsv1.DaemonSet{}
	name := types.NamespacedName{Namespace: utils.LocalOvnkbueNamespace, Name: utils.LocalOvnkbueNodeDsName}
	err := r.Get(context.TODO(), name, ds)
	if err != nil {
		return "", err
	}
	return ds.Spec.Template.Spec.Containers[0].Image, nil
}

func (r *DpuClusterConfigReconciler) syncMachineConfigObjs(cs dpuv1alpha1.DpuClusterConfigSpec) error {
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
		var foundIgn, renderedIgn interface{}
		// The Raw config JSON string may have the fields reordered.
		// For example the "path" field may come before the "contents"
		// field in the rendered ignition JSON; while the found
		// MachineConfig's ignition JSON would have it the other way around.
		// Thus we need to unmarshal the JSON for both found and rendered
		// ignition and compare.
		json.Unmarshal(foundMc.Spec.Config.Raw, &foundIgn)
		json.Unmarshal(mc.Spec.Config.Raw, &renderedIgn)
		if !reflect.DeepEqual(foundIgn, renderedIgn) {
			logger.Info("MachineConfig already exists, updating")
			foundMc.Spec.Config.Raw = mc.Spec.Config.Raw
			mc.SetResourceVersion(foundMc.GetResourceVersion())
			err = r.Update(context.TODO(), mc)
			if err != nil {
				return fmt.Errorf("couldn't update MachineConfig: %v", err)
			}
		} else {
			logger.Info("No content change, skip updating MachineConfig")
		}
	}
	return nil
}

// getMasterAddresses determines the addresses (IP or DNS names) of the ovn-kubernetes
// control plane nodes. It returns the list of addresses and an updated timeout,
// or an error.
func (r *DpuClusterConfigReconciler) getTenantClusterMasterIPs(timeout int) ([]string, int, error) {
	var heartBeat int
	controlPlaneReplicaCount := 3
	c, err := client.New(utils.TenantRestConfig, client.Options{})

	masterNodeList := &corev1.NodeList{}
	ovnMasterAddresses := make([]string, 0, controlPlaneReplicaCount)

	err = wait.PollUntilContextTimeout(context.TODO(), OVN_MASTER_DISCOVERY_POLL*time.Second, time.Duration(timeout)*time.Second, true, func(ctx context.Context) (bool, error) {
		labelSelector := labels.SelectorFromSet(map[string]string{"node-role.kubernetes.io/master": ""}) // TODO remove
		listOps := &client.ListOptions{LabelSelector: labelSelector}

		if err := c.List(ctx, masterNodeList, listOps); err != nil {
			return false, err
		}
		if len(masterNodeList.Items) != 0 && controlPlaneReplicaCount == len(masterNodeList.Items) {
			return true, nil
		}

		heartBeat++
		if heartBeat%3 == 0 {
			logger.Info("Waiting to complete OVN bootstrap: found (%d) master nodes out of (%d) expected: timing out in %d seconds",
				len(masterNodeList.Items), controlPlaneReplicaCount, timeout-OVN_MASTER_DISCOVERY_POLL*heartBeat)
		}
		return false, nil
	})
	if wait.Interrupted(err) {
		logger.Info("Timeout exceeded while bootstraping OVN, expected amount of control plane nodes (%v) do not match found (%v): continuing deployment with found replicas",
			controlPlaneReplicaCount, len(masterNodeList.Items)) // TODO should be a warning
		// On certain types of cluster this condition will never be met (assisted installer, for example)
		// As to not hold the reconciliation loop for too long on such clusters: dynamically modify the timeout
		// to a shorter and shorter value. Never reach 0 however as that will result in a `PollInfinity`.
		// Right now we'll do:
		// - First reconciliation 250 second timeout
		// - Second reconciliation 130 second timeout
		// - >= Third reconciliation 10 second timeout
		if timeout-OVN_MASTER_DISCOVERY_BACKOFF > 0 {
			timeout = timeout - OVN_MASTER_DISCOVERY_BACKOFF
		}
	} else if err != nil {
		return nil, timeout, fmt.Errorf("unable to bootstrap OVN, err: %v", err)
	}

	nodeList := make(nodeInfoList, 0, len(masterNodeList.Items))
	for _, node := range masterNodeList.Items {
		ni := nodeInfo{created: node.CreationTimestamp.Time}
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				ni.address = address.Address
				break
			}
		}
		if ni.address == "" {
			return nil, timeout, fmt.Errorf("no InternalIP found on master node '%s'", node.Name)
		}

		nodeList = append(nodeList, ni)
	}

	// Take the oldest masters up to the expected number of replicas
	sort.Stable(nodeList)
	for i, ni := range nodeList {
		if i >= controlPlaneReplicaCount {
			break
		}
		ovnMasterAddresses = append(ovnMasterAddresses, ni.address)
	}
	logger.Info("Preferring %s for database clusters", ovnMasterAddresses)

	return ovnMasterAddresses, timeout, nil
}

func (r *DpuClusterConfigReconciler) isTenantObjsSynced(ctx context.Context, namespace string) error {
	cm := corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: utils.CmNameOvnCa}, &cm); err != nil {
		return err
	}

	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: utils.CmNameOvnkubeConfig}, &cm); err != nil {
		return err
	}

	s := corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: utils.SecretNameOvnCert}, &s); err != nil {
		return err
	}

	return nil
}

func dbList(masterIPs []string, port string) string {
	addrs := make([]string, len(masterIPs))
	for i, ip := range masterIPs {
		addrs[i] = "ssl:" + net.JoinHostPort(ip, port)
	}
	return strings.Join(addrs, ",")
}

func listenDualStack(masterIP string) string {
	if strings.Contains(masterIP, ":") {
		// IPv6 master, make the databases listen dual-stack
		return ":[::]"
	} else {
		// IPv4 master, be IPv4-only for backward-compatibility
		return ""
	}
}
