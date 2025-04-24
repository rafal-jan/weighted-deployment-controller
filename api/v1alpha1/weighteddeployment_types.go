/*
Copyright 2025 Rafal Jan.

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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Target defines a deployment target with its distribution configuration.
type Target struct {
	// Name of the target, used as suffix for the generated Deployment.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Weight percentage for this target (1-100).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Weight int32 `json:"weight"`

	// NodeSelector is a selector which must be true for the pod to fit on a node.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for this target's pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// Distribution defines how pods should be distributed across targets.
type Distribution struct {
	// Type of distribution strategy.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=WeightBased
	Type string `json:"type"`

	// Targets defines the list of deployment targets.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Targets []Target `json:"targets"`
}

// WeightedDeploymentSpec defines the desired state of WeightedDeployment.
type WeightedDeploymentSpec struct {
	// Number of desired replicas to distribute.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	//TODO: DeplomentSpecTemplate
	// Template is the object that describes the deployment that will be created.
	// +kubebuilder:validation:Required
	Template appsv1.DeploymentSpec `json:"template"`

	// Distribution configuration for the deployment targets.
	// +kubebuilder:validation:Required
	Distribution Distribution `json:"distribution"`
}

// TargetStatus defines the observed state of a deployment target.
type TargetStatus struct {
	// Name of the target.
	Name string `json:"name"`

	// Total number of replicas for this target.
	Replicas int32 `json:"replicas"`

	// Number of ready replicas for this target.
	ReadyReplicas int32 `json:"readyReplicas"`
}

// ManagedDeployment tracks a deployment managed by this controller.
type ManagedDeployment struct {
	// Name of the managed deployment.
	Name string `json:"name"`

	// Number of replicas in the managed deployment.
	Replicas int32 `json:"replicas"`
}

// WeightedDeploymentStatus defines the observed state of WeightedDeployment.
type WeightedDeploymentStatus struct {
	// Total number of replicas for this deployment.
	Replicas int32 `json:"replicas"`

	// Total number of ready replicas for this deployment.
	ReadyReplicas int32 `json:"readyReplicas"`

	// Status information for each target.
	// +optional
	TargetStatus []TargetStatus `json:"targetStatus,omitempty"`

	// References to deployments managed by this controller.
	// +optional
	ManagedDeployments []ManagedDeployment `json:"managedDeployments,omitempty"`

	// Conditions for the weighted deployment.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//TODO: short name `wd` (kubectl get wd)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="Number of desired replicas"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas",description="Number of ready replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// WeightedDeployment is the Schema for the weighteddeployments API.
type WeightedDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WeightedDeploymentSpec   `json:"spec,omitempty"`
	Status WeightedDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WeightedDeploymentList contains a list of WeightedDeployment.
type WeightedDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WeightedDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WeightedDeployment{}, &WeightedDeploymentList{})
}
