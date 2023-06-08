# DPU Network Operator

## Summary

To facilitate the management of Nvidia BlueField-2 DPU, a two-cluster design is
being proposed. Under such design, a BlueField-2 card will be provisioned as a
worker node of the ARM-based infra cluster, whereas the tenant cluster where the
normal user applications run on, is composed of the X86 servers.

The OVN-Kubernetes components are spread over the two clusters. On the tenant
cluster side, the Cluster Network Operator is in charge of the management of the
ovn-kube components. On the infra cluster side, we propose to create a new
operator to be responsible for the life-cycle management of the ovn-kube
components and the necessary host network initialization on DPU cards.

## Quick Start

### Pre-requisites

- An tenant openshift cluster is composed of X86 hosts. The BlueField-2 cards
  are installed on the worker nodes where hardware offloading need to be enabled
- An infra Openshift cluster is composed of ARM hosts. The BlueField-2 cards are
  provisioned as worker nodes of the cluster.
- Pods in infra cluster can reach the API server of tenant cluster

### Run the operator locally

This is designed to run as pod of in the infra cluster. However, we can run it
locally for development purpose.

1. Choose a local namespace where the ovnkube components shall be provisioned.

2. Store the kubeconfig file of the tenant cluster in a Secret

    ```bash
    $ kubectl create secret generic tenant-cluster-1-kubeconf --from-file=config=/root/manifests/kubeconfig.tenant
    ```

3. Create a ConfigMap to store the node specific environment variables.
   
   Example:

    ```yaml
    kind: ConfigMap
    apiVersion: v1
    metadata:
    name: env-overrides
    namespace: default
    data:
    bf2-worker-advnetlab13: |
        TENANT_K8S_NODE=worker-advnetlab13
        MGMT_IFNAME=eth3
    ```

   - `bf2-worker-advnetlab13` is the name of the DPU node.
   - `TENANT_K8S_NODE` is the x86 node name where the DPU is installed.
   - `MGMT_IFNAME` the VF representor name which is used for the `ovn-k8s-mp0`.
4. Start the operator

    ```bash
    $ TENANT_NAMESPACE=openshift-ovn-kubernetes NAMESPACE=default make run
    ```

   - `TENANT_NAMESPACE` specifies the namespace where the ovnkube is running in
     the tenant cluster.
   - `NAMESPACE` specifies the local namespace where the ovnkube components
     shall be deployed.

5. Create an `dpuclusterconfig` custom resource
   Example:

    ```yaml
    apiVersion: dpu.openshift.io/v1alpha1
    kind: DpuClusterConfig
    metadata:
    name: dpuclusterconfig-sample
    namespace: default
    spec:
    kubeConfigFile: tenant-cluster-1-kubeconf
    poolName: dpu
    nodeSelector:
      matchLabels:
        node-role.kubernetes.io/dpu-worker: ""
    ```

   1. `kubeConfigFile` stores the secret name of the tenant cluster kubeconfig
      file. The operator uses this to access the api-server of the tenant
      cluster.
   2. `poolName` specifies the name of the MachineConfigPool CR which contains
      all the BF2 nodes in the infra cluster. 
   3. `nodeSelector` The operator copies it to the `spec.nodeSelector` of MCP.

> **_NOTE:_** By default, the operator will use the ovnkube image of the infra
cluster when generating the ovnkube-node DaemonSet. You can also use environment
variable `OVNKUBE_IMAGE` to specify a particular image you want to use.
