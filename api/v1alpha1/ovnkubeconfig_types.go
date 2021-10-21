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
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// OVNKubeConfigSpec defines the desired state of OVNKubeConfig
type OVNKubeConfigSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// KubeConfigFile is the secret name of the tenant cluster kubeconfig file
	KubeConfigFile string `json:"kubeConfigFile,omitempty"`
	// PoolName is the name of the MachineConfigPool CR which contains
	// the BF2 nodes in the infra cluster.
	PoolName string `json:"poolName"`
}

// OVNKubeConfigStatus defines the observed state of OVNKubeConfig
type OVNKubeConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Conditions represent the latest available observations of an object's state
	Conditions []metav1.Condition `json:"conditions"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// OVNKubeConfig is the Schema for the ovnkubeconfigs API
type OVNKubeConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OVNKubeConfigSpec   `json:"spec,omitempty"`
	Status OVNKubeConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OVNKubeConfigList contains a list of OVNKubeConfig
type OVNKubeConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OVNKubeConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OVNKubeConfig{}, &OVNKubeConfigList{})
}
