/*
Copyright 2026.

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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AlgorithmSpec struct {
	Percentile      int `json:"percentile"`
	HeadroomPercent int `json:"headroomPercent"`
}

type ThresholdsSpec struct {
	MinCPUSavings            int32             `json:"minCpuSavings"`
	MinMemorySavings         resource.Quantity `json:"minMemorySavings"`
	UpsizeUtilizationPercent int               `json:"upsizeUtilizationPercent"`
}

type AutoModeSpec struct {
	Enabled         bool   `json:"enabled"`
	Schedule        string `json:"schedule"`
	RequireApproval bool   `json:"requireApproval"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type RightsizingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RightsizingPolicySpec `json:"spec,omitempty"`
}

type RightsizingPolicySpec struct {
	LookbackDays             int            `json:"lookbackDays"`
	Algorithm                AlgorithmSpec  `json:"algorithm"`
	Thresholds               ThresholdsSpec `json:"thresholds"`
	RevertRetentionDays      int            `json:"revertRetentionDays"`
	AutoMode                 AutoModeSpec   `json:"autoMode"`
	ReconcileIntervalMinutes int            `json:"reconcileIntervalMinutes"`
}

// +kubebuilder:object:root=true
type RightsizingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RightsizingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RightsizingPolicy{}, &RightsizingPolicyList{})
}
