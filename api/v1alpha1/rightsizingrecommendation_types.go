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

type RecommendationDirection string

const (
	DirectionDownsize RecommendationDirection = "downsize"
	DirectionUpsize   RecommendationDirection = "upsize"
)

type RecommendationState string

const (
	StatePending               RecommendationState = "pending"
	StateAwaitingApproval      RecommendationState = "awaiting-approval"
	StateApproved              RecommendationState = "approved"
	StateAppliedPendingRestart RecommendationState = "applied-pending-restart"
	StateApplied               RecommendationState = "applied"
	StateReverted              RecommendationState = "reverted"
	StateFailed                RecommendationState = "failed"
)

type CPUSpec struct {
	Cores   int32 `json:"cores"`
	Sockets int32 `json:"sockets"`
	Threads int32 `json:"threads"`
}

type ResourceSpec struct {
	CPU    CPUSpec           `json:"cpu"`
	Memory resource.Quantity `json:"memory"`
}

type ObjectRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type MetricsSnapshot struct {
	LookbackDays     int     `json:"lookbackDays"`
	CPUP95Percent    float64 `json:"cpuP95Percent"`
	MemoryP95Percent float64 `json:"memoryP95Percent"`
	CPUMaxPercent    float64 `json:"cpuMaxPercent"`
	MemoryMaxPercent float64 `json:"memoryMaxPercent"`
}

type SavingsSpec struct {
	CPU    int32             `json:"cpu"`
	Memory resource.Quantity `json:"memory"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Direction",type=string,JSONPath=`.spec.direction`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="VM",type=string,JSONPath=`.spec.virtualMachineRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RightsizingRecommendation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RightsizingRecommendationSpec   `json:"spec,omitempty"`
	Status RightsizingRecommendationStatus `json:"status,omitempty"`
}

type RightsizingRecommendationSpec struct {
	VirtualMachineRef ObjectRef               `json:"virtualMachineRef"`
	Direction         RecommendationDirection `json:"direction"`
	Current           ResourceSpec            `json:"current"`
	Recommended       ResourceSpec            `json:"recommended"`
	Savings           SavingsSpec             `json:"savings"`
	Metrics           MetricsSnapshot         `json:"metrics"`
	HotplugCapable    bool                    `json:"hotplugCapable"`
	Reason            string                  `json:"reason,omitempty"`
}

type RightsizingRecommendationStatus struct {
	State              RecommendationState `json:"state,omitempty"`
	LastCalculated     *metav1.Time        `json:"lastCalculated,omitempty"`
	AppliedAt          *metav1.Time        `json:"appliedAt,omitempty"`
	ScheduledRestartAt *metav1.Time        `json:"scheduledRestartAt,omitempty"`
	RevertBefore       *metav1.Time        `json:"revertBefore,omitempty"`
	RevertConfig       *ResourceSpec       `json:"revertConfig,omitempty"`
	Message            string              `json:"message,omitempty"`
	Owner              string              `json:"owner,omitempty"`
	ApprovalToken      string              `json:"approvalToken,omitempty"`
	NotifiedAt         *metav1.Time        `json:"notifiedAt,omitempty"`
	ApprovedBy         string              `json:"approvedBy,omitempty"`
	ApprovedAt         *metav1.Time        `json:"approvedAt,omitempty"`
	RejectedBy         string              `json:"rejectedBy,omitempty"`
	RejectedAt         *metav1.Time        `json:"rejectedAt,omitempty"`
	RejectionReason    string              `json:"rejectionReason,omitempty"`
	ServiceNowIncident string              `json:"serviceNowIncident,omitempty"`
	ReminderSentAt     *metav1.Time        `json:"reminderSentAt,omitempty"`
}

// +kubebuilder:object:root=true
type RightsizingRecommendationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RightsizingRecommendation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RightsizingRecommendation{}, &RightsizingRecommendationList{})
}
