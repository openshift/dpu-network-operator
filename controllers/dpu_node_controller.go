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
	"time"

	nmoapiv1beta1 "github.com/medik8s/node-maintenance-operator/api/v1beta1"
	"github.com/openshift/dpu-network-operator/pkg/utils"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Config struct {
	Image               string `envconfig:"IMAGE" default:"quay.io/centos/centos:stream8"`
	ServiceAccount      string `envconfig:"SERVICE_ACCOUNT" default:"dpu-network-operator-controller-manager"`
	SingleClusterDesign bool   `envconfig:"SINGLE_CLUSTER_DESIGN" default:"false"`
}

type DpuNodeLifecycleController struct {
	client.Client
	Config       *Config
	Scheme       *runtime.Scheme
	Log          logrus.FieldLogger
	tenantClient client.Client
	Namespace    string
}

const (
	dpuNodeLabel            = "node-role.kubernetes.io/dpu-worker"
	deploymentPrefix        = "dpu-drain-blocker-"
	maintenancePrefix       = "dpu-tenant-"
	deploymentReplicaNumber = int32(1)
	maxUnAvailableDefault   = int32(0)
)

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=nodemaintenance.medik8s.io,resources=nodemaintenances,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *DpuNodeLifecycleController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithFields(
		logrus.Fields{
			"node_name":          req.Name,
			"operator namespace": r.Namespace,
		})
	defer log.Info("node controller lifecycle reconcile ended")

	var err error
	r.tenantClient, err = r.ensureTenantClient(log)
	// if no tenant client, nothing to do, on error it will retry reconcile
	if err != nil || r.tenantClient == nil {
		return ctrl.Result{}, err
	}

	log.Info("node controller lifecycle reconcile started")
	namespace := r.Namespace
	node := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		log.WithError(err).Errorf("Failed to get node %s", req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if _, hasDpuLabel := node.Labels[dpuNodeLabel]; !hasDpuLabel {
		log.Debugf("Node %s is not dpu, skip", node.Name)
		return ctrl.Result{}, nil
	}

	tenantNode, err := utils.GetMatchedTenantNode(node.Name)
	if err != nil {
		r.Log.WithError(err).Errorf("failed to get tenant node that matches %s", node.Name)
		// in order not to retry forever in case of bad configuration, lets just return
		// changing configmap will in any case require pod restart
		return ctrl.Result{}, nil
	}
	r.Log.Infof("Found tenant node %s", tenantNode)

	if err := r.ensureBlockingDeploymentExists(log, node, namespace); err != nil {
		return ctrl.Result{}, err
	}

	// create pbd before tenant host in order to block drain
	// even if something is wrongly configured
	expectedPDB := r.buildPDB(node, namespace)
	pdb, err := r.getOrCreatePDB(log, node, expectedPDB)
	if err != nil {
		log.WithError(err).Errorf("Failed to get pdb %s", expectedPDB.Name)
		return ctrl.Result{}, err
	}

	tenantShouldBeDrained := r.shouldTenantHostBeDrained(node)
	tenantInRequiredState, err := r.ensureNodeDrainState(tenantNode, tenantShouldBeDrained)
	if err != nil {
		return ctrl.Result{}, err
	}

	expectedPDB.Spec.MaxUnavailable.IntVal = r.getExpectedMaxUnavailable(tenantInRequiredState && tenantShouldBeDrained)
	if err := r.ensurePDBSpecIsAsExpected(log, pdb, expectedPDB); err != nil {
		return ctrl.Result{}, err
	}

	// if tenant should be drained but it was not yet, we should retry reconcile as we don't listen on tenant nodes events
	if !tenantInRequiredState && tenantShouldBeDrained {
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	return ctrl.Result{}, nil
}

// Create deployment with that will run sleep infinity
// This deployment with help of pdb will block drain of dpu node
func (r *DpuNodeLifecycleController) ensureBlockingDeploymentExists(log logrus.FieldLogger, node *corev1.Node, namespace string) error {
	log.Infof("Create blocking deployment if not exists")
	expectedDeployment := r.buildDeployment(node, namespace)
	_, err := utils.GetOrCreateObject(r.Client, expectedDeployment, r.Log)
	return err
}

// return dpu or create one in case it didn't exist
func (r *DpuNodeLifecycleController) getOrCreatePDB(log logrus.FieldLogger, node *corev1.Node, expectedPDB *policyv1.PodDisruptionBudget) (*policyv1.PodDisruptionBudget, error) {
	log.Infof("Get or create blocking pdb for node %s", node.Name)
	pdb, err := utils.GetOrCreateObject(r.Client, expectedPDB, r.Log)
	if err != nil {
		return nil, err
	}
	return pdb.(*policyv1.PodDisruptionBudget), nil
}

// in case dpu is allowed to drain return 1
func (r *DpuNodeLifecycleController) getExpectedMaxUnavailable(allowedToDrain bool) int32 {
	expectedMaxUnavailable := maxUnAvailableDefault
	if allowedToDrain {
		expectedMaxUnavailable = deploymentReplicaNumber
	}
	return expectedMaxUnavailable
}

// Ensure pdb spec was not changed and is same as expected one
// Set MaxUnavailable field in PDB to the expected value
// More than 0 value will allow deployment eviction that will allow dpu to fulfill drain
func (r *DpuNodeLifecycleController) ensurePDBSpecIsAsExpected(log logrus.FieldLogger, pdb, expectedPDB *policyv1.PodDisruptionBudget) error {
	if equality.Semantic.DeepEqual(pdb.Spec, expectedPDB.Spec) {
		log.Infof("No changes in pdb spec, MaxUnavailable is %d", expectedPDB.Spec.MaxUnavailable.IntVal)
		return nil
	}
	pdb.Spec = expectedPDB.Spec
	log.Infof("Setting pdb's spec to %+v", expectedPDB.Spec)
	err := r.Update(context.TODO(), pdb)
	return err
}

func (r *DpuNodeLifecycleController) buildPDB(node *corev1.Node, namespace string) *policyv1.PodDisruptionBudget {
	pdb := policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentPrefix + node.Name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Node",
				Name:       node.Name,
				UID:        node.UID,
				Controller: pointer.Bool(true),
			}},
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &intstr.IntOrString{
				IntVal: maxUnAvailableDefault,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": deploymentPrefix + node.Name,
				},
			},
		},
	}

	return &pdb
}

func (r *DpuNodeLifecycleController) buildDeployment(node *corev1.Node, namespace string) *appsv1.Deployment {
	labels := map[string]string{
		"app": deploymentPrefix + node.Name,
	}
	deployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentPrefix + node.Name,
			Namespace: namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Node",
				Name:       node.Name,
				UID:        node.UID,
				Controller: pointer.Bool(true),
			}},
		},

		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(deploymentReplicaNumber),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					HostNetwork: true,
					Containers: []corev1.Container{
						{
							Name:    "sleep-forever",
							Image:   r.Config.Image,
							Command: []string{"/bin/sh", "-ec", "sleep infinity"},
						},
					},
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": node.Name,
					},
					ServiceAccountName: r.Config.ServiceAccount,
				},
			},
		},
	}

	return &deployment
}

func (r *DpuNodeLifecycleController) shouldTenantHostBeDrained(node *corev1.Node) bool {
	return node.Spec.Unschedulable == true
}

// find the matching node
// build the nmName
// if it should be drained, drain it, if it should be undrained, undrain it.
// return if node is in required state
func (r *DpuNodeLifecycleController) ensureNodeDrainState(tenantNode string, shouldBeDrained bool) (bool, error) {
	nmName := maintenancePrefix + tenantNode
	if shouldBeDrained {
		return r.drainTenantNode(nmName, tenantNode)
	}

	return r.unDrainTenantNode(nmName, tenantNode)
}

// Create nodeMaintenance cr if not created yet
// creating CR will say to NM operator to put node to maintenance/drain
func (r *DpuNodeLifecycleController) drainTenantNode(nmName, tenantHostName string) (bool, error) {
	r.Log.Infof("Start node %s draining", tenantHostName)
	// Create CR if node should be drained and remove if not
	nmAsObj, err := utils.GetOrCreateObject(r.tenantClient, r.buildNodeMaintenanceCR(nmName, tenantHostName), r.Log)
	if err != nil {
		return false, err
	}

	nm := nmAsObj.(*nmoapiv1beta1.NodeMaintenance)
	wasDrained := nm.Status.Phase == nmoapiv1beta1.MaintenanceSucceeded
	if wasDrained {
		r.Log.Infof("Tenant node %s was drained", tenantHostName)
	}

	return wasDrained, nil
}

// Deleting CR will move node from maintenance
// Currently nodemaintenance operator doesn't save previous status of the node, in that case if node previously
// was drained or cordoned it will become uncordon
func (r *DpuNodeLifecycleController) unDrainTenantNode(nmName, tenantHostName string) (bool, error) {
	r.Log.Infof("Start node %s unDraining", tenantHostName)
	nm := &nmoapiv1beta1.NodeMaintenance{}
	typedNM := types.NamespacedName{Name: nmName, Namespace: utils.TenantNamespace}
	err := r.tenantClient.Get(context.TODO(), typedNM, nm)
	if err != nil && !errors.IsNotFound(err) {
		return false, err
	}
	// if nm cr exists we need to delete it
	if err == nil {
		r.Log.Infof("Tenant node %s should be uncordon, deleting NM cr", tenantHostName)
		if err := r.tenantClient.Delete(context.TODO(), nm); err != nil {
			r.Log.WithError(err).Errorf("Failed to delete node maintenance cr %s", nmName)
			return false, err
		}
		r.Log.Infof("Tenant node %s was unDrained", tenantHostName)
	}

	return true, nil
}

func (r *DpuNodeLifecycleController) buildNodeMaintenanceCR(name, tenantNodeHostname string) *nmoapiv1beta1.NodeMaintenance {
	return &nmoapiv1beta1.NodeMaintenance{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: utils.TenantNamespace,
		},
		Spec: nmoapiv1beta1.NodeMaintenanceSpec{
			NodeName: tenantNodeHostname,
			Reason:   "Infra dpu is going to reboot",
		}}
}

// Return client that will handle hosts with dpu status
func (r *DpuNodeLifecycleController) ensureTenantClient(log logrus.FieldLogger) (client.Client, error) {
	if r.tenantClient != nil {
		return r.tenantClient, nil
	}
	if r.Config.SingleClusterDesign {
		log.Infof("Single cluster design is on, tenant client is the same as local")
		return r.Client, nil
	}

	tenantKubeconfig, err := r.getTenantRestClientConfig()
	if err != nil {
		log.WithError(err).Errorf("failed to get tenant kubeconfig")
		return nil, err
	}

	if tenantKubeconfig == nil {
		return nil, nil
	}
	tenantClient, err := client.New(tenantKubeconfig, client.Options{})
	if err != nil {
		r.Log.WithError(err).Errorf("Fail to create client for the tenant cluster")
		return nil, err
	}
	nmoapiv1beta1.AddToScheme(tenantClient.Scheme())
	return tenantClient, err
}

// Since, at this point, it's difficult to set up a fully functioning two-cluster design.
// For that reason, allow to provide tenant-kubeconfig through
// a secret. In this development/debugging mode, the OVNKubeConfigReconciler is disabled
// while the dpu node controller just runs.
func (r *DpuNodeLifecycleController) getTenantRestClientConfig() (*restclient.Config, error) {
	// If TenantRestConfig was set by ovn controller we should use it
	if utils.TenantRestConfig != nil {
		return utils.TenantRestConfig, nil
	}

	tenantKubeconfigName := "tenant-kubeconfig"
	var err error

	s := &corev1.Secret{}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: tenantKubeconfigName, Namespace: utils.Namespace}, s)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Infof("No secret %s in %s, skipping", tenantKubeconfigName, utils.Namespace)
			return nil, nil
		}
		r.Log.WithError(err).Warnf("Failed to get %s though it exists", tenantKubeconfigName)
		return nil, err
	}

	bytes, ok := s.Data["config"]
	if !ok {
		r.Log.WithError(err).Warnf("Failed to get %s, key \"config\" doesn't exist", tenantKubeconfigName)
		return nil, err
	}

	return clientcmd.RESTConfigFromKubeConfig(bytes)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DpuNodeLifecycleController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Owns(&appsv1.Deployment{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Complete(r)
}
