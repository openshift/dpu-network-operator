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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/meta"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// DpuClusterConfigSpec defines the desired state of DpuClusterConfig
type DpuClusterConfigSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// KubeConfigFile is the secret name of the tenant cluster kubeconfig file
	KubeConfigFile string `json:"kubeConfigFile,omitempty"`
	// PoolName is the name of the MachineConfigPool CR which contains
	// the BF2 nodes in the infra cluster.
	PoolName string `json:"poolName"`
	// nodeSelector specifies a label selector for Machines
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
}

// DpuClusterConfigStatus defines the observed state of DpuClusterConfig
type DpuClusterConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Conditions represent the latest available observations of an object's state
	Conditions []metav1.Condition `json:"conditions"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// DpuClusterConfig is the Schema for the dpuclusterconfigs API
type DpuClusterConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DpuClusterConfigSpec   `json:"spec,omitempty"`
	Status DpuClusterConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DpuClusterConfigList contains a list of DpuClusterConfig
type DpuClusterConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DpuClusterConfig `json:"items"`
}

func (cfg *DpuClusterConfig) SetStatus(condition metav1.Condition) {
	meta.SetStatusCondition(&cfg.Status.Conditions, condition)
}

func init() {
	SchemeBuilder.Register(&DpuClusterConfig{}, &DpuClusterConfigList{})
}
