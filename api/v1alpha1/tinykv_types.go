/*
Copyright 2025.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// TinykvSpec defines the desired state of Tinykv.
// TinykvSpec 定义集群的期望状态
type TinykvSpec struct {
	// TinySchedule 组件配置
	TinySchedule TinyScheduleSpec `json:"tinyschedule,omitempty"`

	// TinyKV 组件配置
	TinyKV TinyKVSpec `json:"tinykv,omitempty"`
}

// TinyScheduleSpec 定义 TinySchedule 的资源配置
type TinyScheduleSpec struct {
	// 副本数量
	Replicas *int32 `json:"replicas,omitempty"`

	// 资源限制和请求 (CPU/Memory)
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// 存储配置
	Storage StorageSpec `json:"storage,omitempty"`
}

// TinyKVSpec 定义 TinyKV 的资源配置
type TinyKVSpec struct {
	// 副本数量
	Replicas *int32 `json:"replicas,omitempty"`

	// 资源限制和请求 (CPU/Memory)
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// 存储配置
	Storage StorageSpec `json:"storage,omitempty"`
}

// StorageSpec 定义存储配置
type StorageSpec struct {
	// 存储大小 (例如 "10Gi")
	Size string `json:"size,omitempty"`

	// 存储类名称
	StorageClassName string `json:"storageClassName,omitempty"`
}

// TinykvStatus 定义集群的观测状态
type TinykvStatus struct {
	// 记录 TinySchedule 的状态
	TinyScheduleStatus ComponentStatus `json:"tinyscheduleStatus,omitempty"`

	// 记录 TinyKV 的状态
	TinyKVStatus ComponentStatus `json:"tinykvStatus,omitempty"`
}

// ComponentStatus 定义组件的状态信息
type ComponentStatus struct {
	// 当前副本数
	ReadyReplicas int32 `json:"readyReplicas"`

	// 资源分配情况
	ResourceAllocations corev1.ResourceList `json:"resourceAllocations,omitempty"`

	// 存储卷信息
	PersistentVolumes []string `json:"persistentVolumes,omitempty"`

	// 条件列表 (例如 Available, Progressing)
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Tinykv is the Schema for the tinykvs API.
type Tinykv struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TinykvSpec   `json:"spec,omitempty"`
	Status TinykvStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TinykvList contains a list of Tinykv.
type TinykvList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tinykv `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Tinykv{}, &TinykvList{})
}
