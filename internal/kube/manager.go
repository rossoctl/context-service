package kube

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/rossoctl/context-service/internal/pool"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	poolLabel          = "context.rossoctl.io/pool"
	managedLabel       = "app.kubernetes.io/managed-by"
	managedBy          = "context-service"
	replicasLabel      = "context.rossoctl.io/replicas"
	workspaceName      = "workspace"
	workspaceMount     = "/workspace"
	claimAnnotation    = "context.rossoctl.io/workspace-claim"
	readOnlyAnnotation = "context.rossoctl.io/workspace-read-only"
	replicasAnnotation = "context.rossoctl.io/replicas"
)

var sandboxResource = schema.GroupVersionResource{
	Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes",
}

type Manager struct {
	config  Config
	core    kubernetes.Interface
	dynamic dynamic.Interface
}

func NewManager(config Config) (*Manager, error) {
	coreClient, err := kubernetes.NewForConfig(config.RESTConfig)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(config.RESTConfig)
	if err != nil {
		return nil, err
	}
	return &Manager{config: config, core: coreClient, dynamic: dynamicClient}, nil
}

func (m *Manager) Create(ctx context.Context, request pool.CreateRequest) (pool.Pool, error) {
	if request.Workspace.ClaimName != "" {
		pvc, err := m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).Get(ctx, request.Workspace.ClaimName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return pool.Pool{}, fmt.Errorf("%w: workspace claim %q not found", pool.ErrInvalid, request.Workspace.ClaimName)
		}
		if err != nil {
			return pool.Pool{}, fmt.Errorf("get workspace claim: %w", err)
		}
		if request.Replicas > 1 && !hasAccessMode(pvc, corev1.ReadWriteMany) {
			return pool.Pool{}, fmt.Errorf("%w: workspace claim %q must support ReadWriteMany for multiple sandboxes", pool.ErrInvalid, pvc.Name)
		}
	}

	pvcCount := request.Replicas
	if request.Workspace.ClaimName != "" {
		pvcCount = 0
	} else if request.Workspace.AccessMode == string(corev1.ReadWriteMany) {
		pvcCount = 1
	}
	createdPVCs := make([]string, 0, pvcCount)
	for index := 0; index < pvcCount; index++ {
		pvc := buildPVC(m.config.Namespace, request, index)
		if _, err := m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
			m.rollback(ctx, createdPVCs, nil)
			if apierrors.IsAlreadyExists(err) {
				return pool.Pool{}, pool.ErrAlreadyExists
			}
			return pool.Pool{}, fmt.Errorf("create workspace PVC %s: %w", pvc.Name, err)
		}
		createdPVCs = append(createdPVCs, pvc.Name)
	}

	createdSandboxes := make([]string, 0, request.Replicas)
	for index := 0; index < request.Replicas; index++ {
		sandbox := buildSandbox(m.config, request, index)
		if _, err := m.dynamic.Resource(sandboxResource).Namespace(m.config.Namespace).Create(ctx, sandbox, metav1.CreateOptions{}); err != nil {
			m.rollback(ctx, createdPVCs, createdSandboxes)
			return pool.Pool{}, fmt.Errorf("create sandbox %d: %w", index, err)
		}
		createdSandboxes = append(createdSandboxes, sandbox.GetName())
	}

	return m.Get(ctx, request.Name)
}

func (m *Manager) Get(ctx context.Context, name string) (pool.Pool, error) {
	resources := m.dynamic.Resource(sandboxResource).Namespace(m.config.Namespace)
	sandboxes, err := resources.List(ctx, metav1.ListOptions{LabelSelector: selectorFor(name)})
	if err != nil {
		return pool.Pool{}, fmt.Errorf("list sandboxes: %w", err)
	}
	pvcs, err := m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorFor(name)})
	if err != nil {
		return pool.Pool{}, fmt.Errorf("list workspace PVCs: %w", err)
	}
	if len(pvcs.Items) == 0 && len(sandboxes.Items) == 0 {
		return pool.Pool{}, pool.ErrNotFound
	}
	var pvc *corev1.PersistentVolumeClaim
	replicas := 0
	var readOnly *bool
	claimName := ""
	if len(sandboxes.Items) > 0 {
		annotations := sandboxes.Items[0].GetAnnotations()
		claimName = annotations[claimAnnotation]
		if claimName != "" {
			value, _ := strconv.ParseBool(annotations[readOnlyAnnotation])
			readOnly = &value
		}
		if annotations[replicasAnnotation] != "" {
			replicas, err = strconv.Atoi(annotations[replicasAnnotation])
		} else if len(pvcs.Items) > 0 {
			replicas, err = strconv.Atoi(pvcs.Items[0].Labels[replicasLabel])
		} else {
			err = errors.New("missing replica metadata")
		}
		if claimName != "" {
			pvc, err = m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).Get(ctx, claimName, metav1.GetOptions{})
			if err != nil {
				return pool.Pool{}, fmt.Errorf("get workspace claim: %w", err)
			}
		}
	} else {
		replicas, err = strconv.Atoi(pvcs.Items[0].Labels[replicasLabel])
	}
	if err != nil {
		return pool.Pool{}, errors.New("sandbox pool has invalid replica metadata")
	}
	if pvc == nil {
		pvc = &pvcs.Items[0]
	}
	selector := selectorFor(name)
	pods, err := m.core.CoreV1().Pods(m.config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return pool.Pool{}, fmt.Errorf("list sandbox pods: %w", err)
	}
	ready := 0
	for _, pod := range pods.Items {
		if podReady(pod) {
			ready++
		}
	}
	status := "provisioning"
	if ready == replicas {
		status = "ready"
	}

	storageClass := ""
	if pvc.Spec.StorageClassName != nil {
		storageClass = *pvc.Spec.StorageClassName
	}
	return pool.Pool{
		Name: name, Status: status, Replicas: replicas, ReadyReplicas: ready,
		SandboxSelector: selector,
		Workspace: pool.Workspace{
			Size:       pvc.Spec.Resources.Requests.Storage().String(),
			AccessMode: string(pvc.Spec.AccessModes[0]), StorageClass: storageClass,
			ClaimName: claimName, ReadOnly: readOnly,
		},
	}, nil
}

func (m *Manager) Delete(ctx context.Context, name string) error {
	resources := m.dynamic.Resource(sandboxResource).Namespace(m.config.Namespace)
	list, err := resources.List(ctx, metav1.ListOptions{LabelSelector: selectorFor(name)})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("list sandboxes: %w", err)
	}
	for _, sandbox := range list.Items {
		if err := resources.Delete(ctx, sandbox.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete sandbox %s: %w", sandbox.GetName(), err)
		}
	}
	pvcs, err := m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorFor(name)})
	if err != nil {
		return fmt.Errorf("list workspace PVCs: %w", err)
	}
	if len(pvcs.Items) == 0 && len(list.Items) == 0 {
		return pool.ErrNotFound
	}
	for _, pvc := range pvcs.Items {
		if err := m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete workspace PVC %s: %w", pvc.Name, err)
		}
	}
	return nil
}

func (m *Manager) rollback(ctx context.Context, pvcs, sandboxes []string) {
	resources := m.dynamic.Resource(sandboxResource).Namespace(m.config.Namespace)
	for _, sandbox := range sandboxes {
		_ = resources.Delete(ctx, sandbox, metav1.DeleteOptions{})
	}
	for _, pvc := range pvcs {
		_ = m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).Delete(ctx, pvc, metav1.DeleteOptions{})
	}
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func hasAccessMode(pvc *corev1.PersistentVolumeClaim, mode corev1.PersistentVolumeAccessMode) bool {
	for _, candidate := range pvc.Spec.AccessModes {
		if candidate == mode {
			return true
		}
	}
	return false
}

func buildSandbox(config Config, request pool.CreateRequest, index int) *unstructured.Unstructured {
	labels := map[string]any{poolLabel: request.Name, managedLabel: managedBy}
	claimName := pvcName(request, index)
	readOnly := request.Workspace.ReadOnly != nil && *request.Workspace.ReadOnly
	annotations := map[string]any{
		readOnlyAnnotation: strconv.FormatBool(readOnly), replicasAnnotation: strconv.Itoa(request.Replicas),
	}
	if request.Workspace.ClaimName != "" {
		annotations[claimAnnotation] = claimName
	}
	podSpec := map[string]any{
		"topologySpreadConstraints": []any{map[string]any{
			"maxSkew": int64(1), "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "ScheduleAnyway",
			"labelSelector": map[string]any{"matchLabels": map[string]any{poolLabel: request.Name}},
		}},
		"containers": []any{map[string]any{
			"name": "sandbox", "image": config.SandboxImage,
			"command":      []any{"/bin/sh", "-c", "mkdir -p /workspace && exec sleep infinity"},
			"workingDir":   workspaceMount,
			"volumeMounts": []any{map[string]any{"name": workspaceName, "mountPath": workspaceMount, "readOnly": readOnly}},
			"resources": map[string]any{
				"requests": map[string]any{"memory": "64Mi", "cpu": "50m"},
				"limits":   map[string]any{"memory": "256Mi"},
			},
		}},
		"volumes": []any{map[string]any{
			"name":                  workspaceName,
			"persistentVolumeClaim": map[string]any{"claimName": claimName, "readOnly": readOnly},
		}},
	}
	if config.SandboxServiceAccount != "" {
		podSpec["serviceAccountName"] = config.SandboxServiceAccount
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox",
		"metadata": map[string]any{
			"name": fmt.Sprintf("sandbox-%s-%d", request.Name, index), "namespace": config.Namespace,
			"labels": labels, "annotations": annotations,
		},
		"spec": map[string]any{
			"podTemplate": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     podSpec,
			},
		},
	}}
}
