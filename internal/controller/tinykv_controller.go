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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/google/go-cmp/cmp"
	"github.com/juju/errors"
	kvv1alpha1 "github.com/villanel/api/v1alpha1"
	"github.com/villanel/tinykv-scheduler/kv/raftstore/scheduler_client"
	"github.com/villanel/tinykv-scheduler/proto/pkg/metapb"
)

// TinykvReconciler reconciles a Tinykv object
type TinykvReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	// typeAvailableMemcached represents the status of the Deployment reconciliation
	typeAvailableTischedule = "Available"
)

// +kubebuilder:rbac:groups=kv.villanel.io,resources=tinykvs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kv.villanel.io,resources=tinykvs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kv.villanel.io,resources=tinykvs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Memcached object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *TinykvReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	instance := &kvv1alpha1.Tinykv{}

	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 保存原始状态以比较是否需要更新
	originalStatus := instance.Status.DeepCopy()

	// 1. 部署 TinySchedule
	if err := r.deployTinySchedule(ctx, instance); err != nil {
		logger.Error(err, "Failed to reconcile TinySchedule")
		return ctrl.Result{}, err
	}

	// 2. 检查 TinySchedule 就绪状态
	scheduleReady, result, err := r.checkScheduleReadiness(ctx, instance)
	if err != nil {
		return result, err
	}
	if !scheduleReady {
		if !reflect.DeepEqual(originalStatus, &instance.Status) {
			if err := r.Status().Update(ctx, instance); err != nil {
				logger.Error(err, "Failed to update ScheduleReady status")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	labels := map[string]string{"app": instance.Name}
	selector := &metav1.LabelSelector{MatchLabels: labels}
	instance.Status.Selector = metav1.FormatLabelSelector(selector)
	if err := r.Status().Update(ctx, instance); err != nil {
		logger.Error(err, "Failed to update ScheduleReady status")
		return ctrl.Result{}, err
	}
	// 3. 部署 TinyKV
	if ctrlRes, err := r.deployTinyKV(ctx, instance); err != nil {
		logger.Error(err, "Failed to reconcile TinyKV")
		return ctrlRes, err
	}

	// 4. 检查 TinyKV 就绪状态
	kvReady, resTime, err := r.checkKVReadiness(ctx, instance)
	if err != nil {
		return resTime, err
	}

	if !kvReady {
		// 保存原始状态深拷贝
		originalStatus := instance.Status.DeepCopy()

		// ...修改 instance.Status 的逻辑...

		if !reflect.DeepEqual(originalStatus, &instance.Status) {
			// 记录尝试更新的状态
			attemptedStatus := instance.Status.DeepCopy()

			// 尝试更新状态
			if err := r.Status().Update(ctx, instance); err != nil {
				logger.Error(err, "Failed to update KVReady status")

				// 重新获取最新实例
				latestInstance := &kvv1alpha1.Tinykv{}

				if errGet := r.Get(ctx, req.NamespacedName, latestInstance); errGet != nil {
					logger.Error(errGet, "Failed to re-fetch instance after update failure")
					return ctrl.Result{}, err
				}

				// 打印关键差异信息（使用 JSON 序列化增强可读性）
				attemptedJSON, _ := json.MarshalIndent(attemptedStatus, "", "  ")
				latestJSON, _ := json.MarshalIndent(latestInstance.Status, "", "  ")
				logger.Info("Status conflict detected",
					"attempted", string(attemptedJSON),
					"current", string(latestJSON))

				// 使用 diff 库显示结构化差异
				diff := cmp.Diff(attemptedStatus, &latestInstance.Status,
					cmp.Transformer("Time", func(t metav1.Time) string {
						return t.Format(time.RFC3339)
					}))
				logger.Info("Detailed diff", "difference", diff)

				// 带指数避退的重试
				return ctrl.Result{RequeueAfter: calculateBackoff(err)}, err
			}
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	// 所有组件就绪，更新状态
	if !reflect.DeepEqual(originalStatus, &instance.Status) {
		if err := r.Status().Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to update final status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *TinykvReconciler) deployTinySchedule(ctx context.Context, instance *kvv1alpha1.Tinykv) error {
	logger := log.FromContext(ctx)
	serviceDNS := fmt.Sprintf("tinyschedule.%s.svc.cluster.local", instance.Namespace)

	// 处理副本数默认值
	replicas := int32(1)
	if instance.Spec.TinySchedule.Replicas != nil {
		replicas = *instance.Spec.TinySchedule.Replicas
	}

	// 1. 创建或更新 Headless Service
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tinyschedule",
			Namespace: instance.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		controllerutil.SetControllerReference(instance, service, r.Scheme)
		service.Spec.ClusterIP = corev1.ClusterIPNone
		service.Spec.Selector = map[string]string{"app": "tinyschedule"}
		service.Spec.Ports = []corev1.ServicePort{
			{Name: "client", Port: 2379, TargetPort: intstr.FromInt(2379)},
			{Name: "peer", Port: 2380, TargetPort: intstr.FromInt(2380)},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile Service: %v", err)
	}
	logger.V(1).Info("Service reconciled", "operation", op)

	// 2. 创建或更新 Deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tinyschedule",
			Namespace: instance.Namespace,
		},
	}

	op, err = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		controllerutil.SetControllerReference(instance, deployment, r.Scheme)
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "tinyschedule"},
		}
		deployment.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"app": "tinyschedule"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:    "tinyschedule",
					Image:   "villanel/tinyscheduler-server:latest",
					Command: []string{"/usr/local/bin/tinyscheduler-server"},
					Args: []string{
						"--data-dir=/var/lib/schedule",
						"--client-urls=http://0.0.0.0:2379",
						"--peer-urls=http://0.0.0.0:2380",
						fmt.Sprintf("--advertise-client-urls=http://%s:2379", serviceDNS),
						fmt.Sprintf("--advertise-peer-urls=http://%s:2380", serviceDNS),
					},
					Ports: []corev1.ContainerPort{
						{ContainerPort: 2379, Name: "client"},
						{ContainerPort: 2380, Name: "peer"},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: "/var/lib/schedule"},
					},
					Env: []corev1.EnvVar{{
						Name: "POD_NAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
						},
					}},
					Resources: instance.Spec.TinySchedule.Resources,
				}},
				// 添加Volumes字段引用PVC
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "tinyschedule-pvc", // PVC名称需要与下面创建的PVC一致
							},
						},
					},
				},
			},
		}
		return nil
	})

	// 创建或更新PVC（需要在同一个Reconcile方法中处理）
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tinyschedule-pvc",
			Namespace: instance.Namespace,
		},
	}
	_, pvcErr := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		// 设置ControllerReference
		if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
			return err
		}
		if instance.Spec.TinySchedule.Storage.Size == "" {
			instance.Spec.TinySchedule.Storage.Size = "10Gi"
		}
		if pvc.CreationTimestamp.IsZero() {
			storageClassName := instance.Spec.TinySchedule.Storage.StorageClassName
			storageSize := resource.MustParse(instance.Spec.TinySchedule.Storage.Size)

			pvc.Spec = corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
				StorageClassName: &storageClassName,
			}
			return nil
		}

		// 已有PVC：只允许修改存储大小
		newSize := resource.MustParse(instance.Spec.TinySchedule.Storage.Size)
		if newSize.Cmp(pvc.Spec.Resources.Requests[corev1.ResourceStorage]) > 0 {
			pvc.Spec.Resources.Requests[corev1.ResourceStorage] = newSize
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile Deployment: %v", err)
	}
	if pvcErr != nil {
		return fmt.Errorf("failed to reconcile Deployment: %v", pvcErr)
	}
	logger.V(1).Info("Deployment reconciled", "operation", op)

	return nil
}

func (r *TinykvReconciler) checkScheduleReadiness(ctx context.Context, instance *kvv1alpha1.Tinykv) (bool, ctrl.Result, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      "tinyschedule",
		Namespace: instance.Namespace,
	}, deployment); err != nil {
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&instance.Status.TinyScheduleStatus.Conditions, metav1.Condition{
				Type:    "Available",
				Status:  metav1.ConditionFalse,
				Reason:  "DeploymentNotFound",
				Message: "TinySchedule Deployment not found",
			})
			return false, ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return false, ctrl.Result{}, err
	}

	expectedReplicas := int32(1)
	if instance.Spec.TinySchedule.Replicas != nil {
		expectedReplicas = *instance.Spec.TinySchedule.Replicas
	}

	available := deployment.Status.AvailableReplicas
	updated := deployment.Status.UpdatedReplicas
	ready := available >= expectedReplicas && updated == expectedReplicas

	statusCondition := metav1.Condition{
		Type:    "Available",
		Status:  metav1.ConditionTrue,
		Reason:  "AllReplicasReady",
		Message: fmt.Sprintf("%d/%d pods ready", available, expectedReplicas),
	}
	if !ready {
		statusCondition.Status = metav1.ConditionFalse
		statusCondition.Reason = "ReplicasNotReady"
		statusCondition.Message = fmt.Sprintf("%d/%d pods ready", available, expectedReplicas)
	}
	meta.SetStatusCondition(&instance.Status.TinyScheduleStatus.Conditions, statusCondition)

	// 更新状态字段
	instance.Status.TinyScheduleStatus.ReadyReplicas = deployment.Status.ReadyReplicas
	instance.Status.TinyScheduleStatus.ResourceAllocations = calculateResourceAllocations(deployment)
	instance.Status.TinyScheduleStatus.PersistentVolumes, _ = getPersistentVolumes(ctx, r.Client, instance.Namespace, "tinyschedule")

	return ready, ctrl.Result{}, nil
}

func (r *TinykvReconciler) deployTinyKV(ctx context.Context, instance *kvv1alpha1.Tinykv) (ctrl.Result, error) {

	logger := log.FromContext(ctx)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tinykv",
			Namespace: instance.Namespace,
		},
	}

	svcOp, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		controllerutil.SetControllerReference(instance, svc, r.Scheme)
		svc.Spec.ClusterIP = corev1.ClusterIPNone // Headless Service
		labels := map[string]string{"app": instance.Name}
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "client",
				Port:       20160,
				TargetPort: intstr.FromInt(20160),
			},
			{
				Name:       "peer",
				Port:       2380,
				TargetPort: intstr.FromInt(2380),
			},
		}
		return nil
	})

	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Service: %v", err)
	}
	logger.V(1).Info("Service reconciled", "operation", svcOp)
	// 处理副本数默认值
	replicas := int32(1)
	if instance.Spec.TinyKV.Replicas != nil {
		replicas = *instance.Spec.TinyKV.Replicas
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tinykv",
			Namespace: instance.Namespace,
		},
	}

	// 获取现有StatefulSet信息
	existingSts := &appsv1.StatefulSet{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(sts), existingSts)
	var (
		oldReplicas int32 = 0
		stsExists   bool  = true
	)

	// 处理StatefulSet查询结果
	if err != nil {
		if apierrors.IsNotFound(err) {
			fmt.Println("errrrrrrrrrrrrrrrrrrrrrrrrr")
			// 标记StatefulSet不存在
			stsExists = false
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to check existing StatefulSet: %w", err)
		}
	} else {
		// 获取当前副本数（处理未显式设置的情况）
		if existingSts.Spec.Replicas != nil {
			oldReplicas = *existingSts.Spec.Replicas
		} else {
			oldReplicas = 1 // Kubernetes默认值
		}
	}

	// 当StatefulSet存在且需要缩容时处理
	newReplicas := *instance.Spec.TinyKV.Replicas
	if stsExists && newReplicas < oldReplicas {
		if err := r.handleScaleDown(ctx, instance, int(oldReplicas), int(newReplicas)); err != nil {
			return ctrl.Result{}, fmt.Errorf("scale down handler failed: %w", err)
		}
	}
	newSts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tinykv",
			Namespace: instance.Namespace,
		},
	}

	labels := map[string]string{"app": instance.Name}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, newSts, func() error {
		controllerutil.SetControllerReference(instance, newSts, r.Scheme)
		newSts.Spec.ServiceName = "tinykv"
		newSts.Spec.Replicas = &replicas
		newSts.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: labels,
		}
		newSts.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:    "tinykv",
					Image:   "villanel/tinykv-server:latest",
					Command: []string{"/usr/local/bin/tinykv-server"},
					Args: []string{
						"--addr=$(POD_NAME).tinykv.$(NAMESPACE).svc.cluster.local:20160",
						"--path=/var/lib/tinykv",
						"--scheduler=tinyschedule.$(NAMESPACE).svc.cluster.local:2379",
					},
					Env: []corev1.EnvVar{
						{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.name",
								},
							},
						},
						{
							Name: "NAMESPACE",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.namespace",
								},
							},
						},
					},
					Ports: []corev1.ContainerPort{
						{ContainerPort: 20160, Name: "client"},
						{ContainerPort: 2380, Name: "peer"},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: "/var/lib/tinykv"},
					},
					Resources: instance.Spec.TinyKV.Resources,
				}},
			},
		}
		// 设置VolumeClaimTemplates
		storageClassName := instance.Spec.TinyKV.Storage.StorageClassName
		storageSize := resource.MustParse(instance.Spec.TinyKV.Storage.Size)
		newSts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "data",
					Labels: map[string]string{"app": "tinykv"},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: storageSize,
						},
					},
					StorageClassName: &storageClassName,
				},
			},
		}
		newSts.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
			WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		}
		return nil
	})

	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile StatefulSet: %v", err)
	}
	logger.V(1).Info("StatefulSet reconciled", "operation", op)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-hpa", // HPA 名称：<instance-name>-hpa
			Namespace: instance.Namespace,
		},
	}

	// 固定 HPA 参数（可根据需求调整）
	minReplicas := int32(5)
	maxReplicas := int32(10)
	targetCPUUtilization := int32(80)
	// 创建或更新 HPA
	op, err = controllerutil.CreateOrUpdate(ctx, r.Client, hpa, func() error {
		controllerutil.SetControllerReference(instance, hpa, r.Scheme) // 绑定 OwnerReference
		hpa.Spec = autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: instance.APIVersion,
				Kind:       instance.Kind,
				Name:       instance.Name, // 指向对应的 StatefulSet
			},
			MinReplicas: &minReplicas,
			MaxReplicas: maxReplicas,
			Metrics: []autoscalingv2.MetricSpec{
				{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{
							Type:               autoscalingv2.UtilizationMetricType,
							AverageUtilization: &targetCPUUtilization,
						},
					},
				},
			},
		}
		return nil
	})

	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile HPA: %v", err)
	}
	logger.V(1).Info("HPA reconciled", "operation", op)

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
func (r *TinykvReconciler) handleScaleDown(
	ctx context.Context,
	instance *kvv1alpha1.Tinykv,
	oldReplicas, newReplicas int,
) error {
	// 创建调度客户端
	schedulerClient, err := scheduler_client.NewClient(strings.Split("tinyschedule.default.svc.cluster.local:2379", ","), "")
	if err != nil {
		return err
	}
	defer schedulerClient.Close()
	println("OldReplicas", oldReplicas)
	println("newReplicas", newReplicas)
	// 获取所有存储节点信息
	stores, err := schedulerClient.GetAllStores(ctx)
	if err != nil || len(stores) == 0 {
		return fmt.Errorf("failed to get stores: %v", err)
	}

	// 构建Pod序号到StoreID的映射

	storeMap := make(map[int]uint64)
	// 1. 构建Pod序号到StoreID的映射
	for _, store := range stores {
		matches := regexp.MustCompile(`tinykv-(\d+)`).FindStringSubmatch(store.Address)
		if len(matches) < 2 {
			fmt.Printf("WARN: invalid store address format: %s\n", store.Address)

			continue
		}

		index, err := strconv.Atoi(matches[1])
		if err != nil {
			fmt.Printf("WARN: invalid pod index in address %s: %v\n", store.Address, err)
			continue
		}
		storeMap[index] = store.GetId()
	}

	// 2. 动态确定需要清理的Pod范围
	var candidates []int
	for podIndex := range storeMap {
		if podIndex >= newReplicas { // 处理所有>=新副本数的Pod
			candidates = append(candidates, podIndex)
		}
	}
	// 降序排序确保从最高序号开始处理
	sort.Sort(sort.Reverse(sort.IntSlice(candidates)))

	// 3. 执行清理操作
	var errs []error
	for _, podIndex := range candidates {
		storeID := storeMap[podIndex]
		fmt.Printf("DEBUG: Processing pod %d (store %d)\n", podIndex, storeID)

		store, err := schedulerClient.GetStore(ctx, storeID)
		if err != nil {
			errs = append(errs, fmt.Errorf("get store %d (pod %d) failed: %v", storeID, podIndex, err))
			continue
		}

		switch store.GetState() {
		case metapb.StoreState_Up:
			if _, err := schedulerClient.OfflineStore(ctx, storeID); err != nil {
				errs = append(errs, fmt.Errorf("offline store %d (pod %d) failed: %v", storeID, podIndex, err))
				continue
			}
			if err := waitForStoreTombstone(ctx, schedulerClient, storeID); err != nil {
				errs = append(errs, fmt.Errorf("wait tombstone for store %d (pod %d) failed: %v", storeID, podIndex, err))
				continue
			}
			fallthrough

		case metapb.StoreState_Tombstone:
			if _, err := schedulerClient.RemoveStore(ctx, storeID); err != nil {
				errs = append(errs, fmt.Errorf("remove store %d (pod %d) failed: %v", storeID, podIndex, err))
			}

		case metapb.StoreState_Offline:
			if err := waitForStoreTombstone(ctx, schedulerClient, storeID); err != nil {
				errs = append(errs, fmt.Errorf("wait offline store %d (pod %d) failed: %v", storeID, podIndex, err))
				continue
			}
			if _, err := schedulerClient.RemoveStore(ctx, storeID); err != nil {
				errs = append(errs, fmt.Errorf("remove offline store %d (pod %d) failed: %v", storeID, podIndex, err))
			}

		default:
			errs = append(errs, fmt.Errorf("store %d (pod %d) in unexpected state: %s",
				storeID, podIndex, store.GetState().String()))
		}
	}

	// 4. 统一处理错误
	if len(errs) > 0 {
		return &CleanError{Errors: errs}
	}
	return nil
}

type CleanError struct {
	Errors []error
}

func (e *CleanError) Error() string {
	var sb strings.Builder
	sb.WriteString("store cleanup errors:\n")
	for _, err := range e.Errors {
		sb.WriteString(fmt.Sprintf("• %s\n", err.Error()))
	}
	return sb.String()
}

// 等待存储节点变为Tombstone状态（需要实现超时和重试）
func waitForStoreTombstone(ctx context.Context, client scheduler_client.Client, storeID uint64) error {
	return wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		store, err := client.GetStore(ctx, storeID)
		if err != nil {
			return false, err
		}
		return store.GetState() == metapb.StoreState_Tombstone, nil
	})
}
func (r *TinykvReconciler) checkKVReadiness(ctx context.Context, instance *kvv1alpha1.Tinykv) (bool, ctrl.Result, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      "tinykv",
		Namespace: instance.Namespace,
	}, sts); err != nil {
		if apierrors.IsNotFound(err) {
			meta.SetStatusCondition(&instance.Status.TinyKVStatus.Conditions, metav1.Condition{
				Type:    "Available",
				Status:  metav1.ConditionFalse,
				Reason:  "StatefulSetNotFound",
				Message: "TinyKV StatefulSet not found",
			})
			return false, ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return false, ctrl.Result{}, err
	}

	expectedReplicas := int32(1)
	if instance.Spec.TinyKV.Replicas != nil {
		expectedReplicas = *instance.Spec.TinyKV.Replicas
	}

	ready := sts.Status.ReadyReplicas >= expectedReplicas &&
		sts.Status.UpdatedReplicas == expectedReplicas

	statusCondition := metav1.Condition{
		Type:    "Available",
		Status:  metav1.ConditionTrue,
		Reason:  "AllReplicasReady",
		Message: fmt.Sprintf("%d/%d pods ready", sts.Status.ReadyReplicas, expectedReplicas),
	}
	if !ready {
		statusCondition.Status = metav1.ConditionFalse
		statusCondition.Reason = "ReplicasNotReady"
		statusCondition.Message = fmt.Sprintf("%d/%d pods ready", sts.Status.ReadyReplicas, expectedReplicas)
	}
	meta.SetStatusCondition(&instance.Status.TinyKVStatus.Conditions, statusCondition)

	// 更新状态字段
	instance.Status.TinyKVStatus.ReadyReplicas = sts.Status.ReadyReplicas
	instance.Status.TinyKVStatus.ResourceAllocations = calculateResourceAllocations(sts)
	instance.Status.TinyKVStatus.PersistentVolumes, _ = getPersistentVolumes(ctx, r.Client, instance.Namespace, "tinykv")

	return ready, ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// 增强资源计算（支持多资源类型和初始化请求）
func calculateResourceAllocations(obj metav1.Object) corev1.ResourceList {
	total := corev1.ResourceList{}

	var containers []corev1.Container
	switch v := obj.(type) {
	case *appsv1.Deployment:
		containers = v.Spec.Template.Spec.Containers
	case *appsv1.StatefulSet:
		containers = v.Spec.Template.Spec.Containers
	default:
		return total
	}

	for _, container := range containers {
		for res, quantity := range container.Resources.Requests {
			if existing, exists := total[res]; exists {
				existing.Add(quantity)
				total[res] = existing
			} else {
				total[res] = quantity.DeepCopy()
			}
		}
	}
	return total
}

// 改进的持久卷查询（增加错误处理和标签过滤）
func getPersistentVolumes(ctx context.Context, c client.Client, namespace, component string) ([]string, error) {
	pvcList := &corev1.PersistentVolumeClaimList{}
	err := c.List(ctx, pvcList,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"app":       "tinykv",
			"component": component,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list PVCs: %w", err)
	}

	pvs := make([]string, 0, len(pvcList.Items))
	for _, pvc := range pvcList.Items {
		if pvc.Status.Phase == corev1.ClaimBound && pvc.Spec.VolumeName != "" {
			pvs = append(pvs, pvc.Spec.VolumeName)
		}
	}
	return pvs, nil
}

// 增强的控制器注册（添加状态集监控和事件过滤）
func (r *TinykvReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kvv1alpha1.Tinykv{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		WithEventFilter(predicate.Or(
			predicate.LabelChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			predicate.GenerationChangedPredicate{},
		)).
		Named("tinykv-operator").
		Complete(r)
}
func calculateBackoff(err error) time.Duration {
	if apierrors.IsConflict(err) {
		return 1 * time.Second // 冲突快速重试
	}
	return 5 * time.Second // 其他错误常规间隔
}
