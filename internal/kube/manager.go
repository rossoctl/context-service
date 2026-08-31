package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
	"github.com/rossoctl/context-service/internal/storageclass"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultStorageClassAnnotation     = "storageclass.kubernetes.io/is-default-class"
	betaDefaultStorageClassAnnotation = "storageclass.beta.kubernetes.io/is-default-class"
)

func (m *Manager) ListStorageClasses(ctx context.Context) ([]storageclass.Resource, error) {
	classes, err := m.core.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list storage classes: %w", err)
	}
	items := make([]storageclass.Resource, 0, len(classes.Items))
	for i := range classes.Items {
		class := &classes.Items[i]
		bindingMode := string(storagev1.VolumeBindingImmediate)
		if class.VolumeBindingMode != nil {
			bindingMode = string(*class.VolumeBindingMode)
		}
		reclaimPolicy := string(corev1.PersistentVolumeReclaimDelete)
		if class.ReclaimPolicy != nil {
			reclaimPolicy = string(*class.ReclaimPolicy)
		}
		items = append(items, storageclass.Resource{
			Name:                 class.Name,
			Default:              class.Annotations[defaultStorageClassAnnotation] == "true" || class.Annotations[betaDefaultStorageClassAnnotation] == "true",
			Provisioner:          class.Provisioner,
			VolumeBindingMode:    bindingMode,
			ReclaimPolicy:        reclaimPolicy,
			AllowVolumeExpansion: class.AllowVolumeExpansion != nil && *class.AllowVolumeExpansion,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

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
	profileAnnotation  = "context.rossoctl.io/sandbox-profile"
	contextLabel       = "context.rossoctl.io/name"
	contextTypeLabel   = "context.rossoctl.io/type"
)

var sandboxResource = schema.GroupVersionResource{
	Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes",
}

func (m *Manager) CreateContext(ctx context.Context, request contextresource.CreateRequest) (contextresource.Resource, error) {
	pvc := buildContextPVC(request)
	_, err := m.core.CoreV1().PersistentVolumeClaims(request.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return contextresource.Resource{}, contextresource.ErrAlreadyExists
	}
	if err != nil {
		return contextresource.Resource{}, fmt.Errorf("create context PVC: %w", err)
	}
	return m.GetContext(ctx, request.Namespace, request.Name)
}

func (m *Manager) GetContext(ctx context.Context, namespace, name string) (contextresource.Resource, error) {
	pvc, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, contextPVCName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return contextresource.Resource{}, contextresource.ErrNotFound
	}
	if err != nil {
		return contextresource.Resource{}, fmt.Errorf("get context PVC: %w", err)
	}
	if pvc.Labels[managedLabel] != managedBy || pvc.Labels[contextLabel] != name {
		return contextresource.Resource{}, contextresource.ErrNotFound
	}
	return resourceFromContextPVC(pvc), nil
}

func (m *Manager) ListContexts(ctx context.Context, namespace string) ([]contextresource.Resource, error) {
	pvcs, err := m.core.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: managedLabel + "=" + managedBy,
	})
	if err != nil {
		return nil, fmt.Errorf("list context PVCs: %w", err)
	}
	items := make([]contextresource.Resource, 0, len(pvcs.Items))
	for index := range pvcs.Items {
		if pvcs.Items[index].Labels[contextLabel] == "" {
			continue
		}
		items = append(items, resourceFromContextPVC(&pvcs.Items[index]))
	}
	return items, nil
}

func resourceFromContextPVC(pvc *corev1.PersistentVolumeClaim) contextresource.Resource {
	storageClass := ""
	if pvc.Spec.StorageClassName != nil {
		storageClass = *pvc.Spec.StorageClassName
	}
	status := "provisioning"
	if pvc.Status.Phase == corev1.ClaimBound {
		status = "ready"
	}
	return contextresource.Resource{
		Name: pvc.Labels[contextLabel], Namespace: pvc.Namespace, Type: pvc.Labels[contextTypeLabel], Status: status,
		Storage: contextresource.Storage{
			Backend: "pvc", Size: pvc.Spec.Resources.Requests.Storage().String(),
			AccessMode: string(pvc.Spec.AccessModes[0]), StorageClass: storageClass,
		},
		Attachment: contextresource.Attachment{Kind: "pvc", ClaimName: pvc.Name},
	}
}

func (m *Manager) DeleteContext(ctx context.Context, namespace, name string) error {
	pvc, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, contextPVCName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return contextresource.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get context PVC: %w", err)
	}
	if pvc.Labels[managedLabel] != managedBy || pvc.Labels[contextLabel] != name {
		return contextresource.ErrNotFound
	}
	if err := m.core.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete context PVC: %w", err)
	}
	return nil
}

var sandboxClaimResource = schema.GroupVersionResource{
	Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxclaims",
}

var sandboxWarmPoolResource = schema.GroupVersionResource{
	Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxwarmpools",
}

var sandboxTemplateResource = schema.GroupVersionResource{
	Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxtemplates",
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
	if request.WarmPoolRef != "" {
		return m.createClaims(ctx, request)
	}
	profile, err := m.resolveSandboxProfile(ctx, request.SandboxProfile)
	if err != nil {
		return pool.Pool{}, err
	}
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
		sandbox, err := buildSandboxWithProfile(m.config, request, index, profile)
		if err != nil {
			m.rollback(ctx, createdPVCs, createdSandboxes)
			return pool.Pool{}, err
		}
		if _, err := m.dynamic.Resource(sandboxResource).Namespace(m.config.Namespace).Create(ctx, sandbox, metav1.CreateOptions{}); err != nil {
			m.rollback(ctx, createdPVCs, createdSandboxes)
			return pool.Pool{}, fmt.Errorf("create sandbox %d: %w", index, err)
		}
		createdSandboxes = append(createdSandboxes, sandbox.GetName())
	}

	return m.Get(ctx, request.Name)
}

func (m *Manager) List(ctx context.Context) ([]pool.Pool, error) {
	names := map[string]struct{}{}
	collect := func(labels map[string]string) {
		if name := labels[poolLabel]; name != "" {
			names[name] = struct{}{}
		}
	}

	claims, err := m.dynamic.Resource(sandboxClaimResource).Namespace(m.config.Namespace).List(
		ctx, metav1.ListOptions{LabelSelector: managedLabel + "=" + managedBy},
	)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("list sandbox claims: %w", err)
	}
	if err == nil {
		for _, claim := range claims.Items {
			collect(claim.GetLabels())
		}
	}

	sandboxes, err := m.dynamic.Resource(sandboxResource).Namespace(m.config.Namespace).List(
		ctx, metav1.ListOptions{LabelSelector: managedLabel + "=" + managedBy},
	)
	if err != nil {
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	for _, sandbox := range sandboxes.Items {
		collect(sandbox.GetLabels())
	}

	pvcs, err := m.core.CoreV1().PersistentVolumeClaims(m.config.Namespace).List(
		ctx, metav1.ListOptions{LabelSelector: managedLabel + "=" + managedBy},
	)
	if err != nil {
		return nil, fmt.Errorf("list workspace PVCs: %w", err)
	}
	for _, pvc := range pvcs.Items {
		collect(pvc.Labels)
	}

	orderedNames := make([]string, 0, len(names))
	for name := range names {
		orderedNames = append(orderedNames, name)
	}
	sort.Strings(orderedNames)

	items := make([]pool.Pool, 0, len(orderedNames))
	for _, name := range orderedNames {
		item, err := m.Get(ctx, name)
		if errors.Is(err, pool.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get sandbox pool %s: %w", name, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func (m *Manager) Get(ctx context.Context, name string) (pool.Pool, error) {
	claims, err := m.dynamic.Resource(sandboxClaimResource).Namespace(m.config.Namespace).List(
		ctx, metav1.ListOptions{LabelSelector: selectorFor(name)},
	)
	if err == nil && len(claims.Items) > 0 {
		result, err := poolFromClaims(name, claims.Items)
		if err != nil {
			return pool.Pool{}, err
		}
		for _, claim := range claims.Items {
			result.Resources = append(result.Resources, pool.KubernetesResource{
				Kind: "sandboxclaim", Name: claim.GetName(), Status: conditionStatus(claim, "Ready"),
			})
		}
		pods, err := m.core.CoreV1().Pods(m.config.Namespace).List(
			ctx, metav1.ListOptions{LabelSelector: selectorFor(name)},
		)
		if err != nil {
			return pool.Pool{}, fmt.Errorf("list sandbox pods: %w", err)
		}
		for _, pod := range pods.Items {
			result.Resources = append(result.Resources, podResource(pod))
		}
		sortPoolResources(result.Resources)
		return result, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return pool.Pool{}, fmt.Errorf("list sandbox claims: %w", err)
	}

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
	sandboxProfile := ""
	if len(sandboxes.Items) > 0 {
		annotations := sandboxes.Items[0].GetAnnotations()
		claimName = annotations[claimAnnotation]
		sandboxProfile = annotations[profileAnnotation]
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
	result := pool.Pool{
		Name: name, Status: status, Replicas: replicas, ReadyReplicas: ready,
		SandboxSelector: selector, SandboxProfile: sandboxProfile,
		Workspace: pool.Workspace{
			Size:       pvc.Spec.Resources.Requests.Storage().String(),
			AccessMode: string(pvc.Spec.AccessModes[0]), StorageClass: storageClass,
			ClaimName: claimName, ReadOnly: readOnly,
		},
	}
	for _, sandbox := range sandboxes.Items {
		result.Resources = append(result.Resources, pool.KubernetesResource{
			Kind: "sandbox", Name: sandbox.GetName(), Status: conditionStatus(sandbox, "Ready"),
		})
	}
	for _, pod := range pods.Items {
		result.Resources = append(result.Resources, podResource(pod))
	}
	for _, workspacePVC := range pvcs.Items {
		result.Resources = append(result.Resources, pvcResource(workspacePVC))
	}
	if claimName != "" {
		result.Resources = append(result.Resources, pvcResource(*pvc))
	}
	sortPoolResources(result.Resources)
	return result, nil
}

func conditionStatus(resource unstructured.Unstructured, conditionType string) string {
	conditions, _, _ := unstructured.NestedSlice(resource.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok || condition["type"] != conditionType {
			continue
		}
		if condition["status"] == "True" {
			return conditionType
		}
		if reason, ok := condition["reason"].(string); ok && reason != "" {
			return reason
		}
	}
	return "Provisioning"
}

func podResource(pod corev1.Pod) pool.KubernetesResource {
	status := string(pod.Status.Phase)
	if status == "" {
		status = "Pending"
	}
	return pool.KubernetesResource{Kind: "pod", Name: pod.Name, Status: status}
}

func pvcResource(pvc corev1.PersistentVolumeClaim) pool.KubernetesResource {
	status := string(pvc.Status.Phase)
	if status == "" {
		status = "Pending"
	}
	return pool.KubernetesResource{Kind: "pvc", Name: pvc.Name, Status: status}
}

func sortPoolResources(resources []pool.KubernetesResource) {
	order := map[string]int{"sandboxclaim": 0, "sandbox": 1, "pod": 2, "pvc": 3}
	sort.Slice(resources, func(i, j int) bool {
		if order[resources[i].Kind] != order[resources[j].Kind] {
			return order[resources[i].Kind] < order[resources[j].Kind]
		}
		return resources[i].Name < resources[j].Name
	})
}

func (m *Manager) Delete(ctx context.Context, name string) error {
	claims := m.dynamic.Resource(sandboxClaimResource).Namespace(m.config.Namespace)
	claimList, err := claims.List(ctx, metav1.ListOptions{LabelSelector: selectorFor(name)})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("list sandbox claims: %w", err)
	}
	if err == nil && len(claimList.Items) > 0 {
		for _, claim := range claimList.Items {
			propagation := metav1.DeletePropagationForeground
			if err := claims.Delete(ctx, claim.GetName(), metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete sandbox claim %s: %w", claim.GetName(), err)
			}
		}
		return nil
	}

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

func (m *Manager) createClaims(ctx context.Context, request pool.CreateRequest) (pool.Pool, error) {
	warmPools := m.dynamic.Resource(sandboxWarmPoolResource).Namespace(m.config.Namespace)
	if _, err := warmPools.Get(ctx, request.WarmPoolRef, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return pool.Pool{}, fmt.Errorf("%w: warm pool %q not found", pool.ErrInvalid, request.WarmPoolRef)
		}
		return pool.Pool{}, fmt.Errorf("get warm pool: %w", err)
	}

	claims := m.dynamic.Resource(sandboxClaimResource).Namespace(m.config.Namespace)
	created := make([]string, 0, request.Replicas)
	for index := 0; index < request.Replicas; index++ {
		claim := buildSandboxClaim(m.config.Namespace, request, index)
		if _, err := claims.Create(ctx, claim, metav1.CreateOptions{}); err != nil {
			for _, name := range created {
				_ = claims.Delete(ctx, name, metav1.DeleteOptions{})
			}
			if apierrors.IsAlreadyExists(err) {
				return pool.Pool{}, pool.ErrAlreadyExists
			}
			return pool.Pool{}, fmt.Errorf("create sandbox claim %d: %w", index, err)
		}
		created = append(created, claim.GetName())
	}
	return m.Get(ctx, request.Name)
}

func poolFromClaims(name string, claims []unstructured.Unstructured) (pool.Pool, error) {
	warmPoolRef, _, err := unstructured.NestedString(claims[0].Object, "spec", "warmPoolRef", "name")
	if err != nil || warmPoolRef == "" {
		return pool.Pool{}, errors.New("sandbox claim has invalid warm pool reference")
	}
	ready := 0
	for _, claim := range claims {
		conditions, _, _ := unstructured.NestedSlice(claim.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, ok := raw.(map[string]any)
			if ok && condition["type"] == "Ready" && condition["status"] == "True" {
				ready++
				break
			}
		}
	}
	status := "provisioning"
	if ready == len(claims) {
		status = "ready"
	}
	return pool.Pool{
		Name: name, Status: status, Replicas: len(claims), ReadyReplicas: ready,
		SandboxSelector: selectorFor(name), WarmPoolRef: warmPoolRef,
	}, nil
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
	return buildDefaultSandbox(config, request, index)
}

func buildDefaultSandbox(config Config, request pool.CreateRequest, index int) *unstructured.Unstructured {
	labels := map[string]any{poolLabel: request.Name, managedLabel: managedBy}
	claimName := pvcName(request, index)
	readOnly := request.Workspace.ReadOnly != nil && *request.Workspace.ReadOnly
	annotations := sandboxAnnotations(request, claimName, readOnly)
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
	return sandboxResourceObject(config.Namespace, request, index, labels, annotations, map[string]any{
		"podTemplate": map[string]any{
			"metadata": map[string]any{"labels": labels},
			"spec":     podSpec,
		},
	})
}

func sandboxAnnotations(request pool.CreateRequest, claimName string, readOnly bool) map[string]any {
	annotations := map[string]any{
		readOnlyAnnotation: strconv.FormatBool(readOnly), replicasAnnotation: strconv.Itoa(request.Replicas),
	}
	if request.Workspace.ClaimName != "" {
		annotations[claimAnnotation] = claimName
	}
	if request.SandboxProfile != "" {
		annotations[profileAnnotation] = request.SandboxProfile
	}
	return annotations
}

func sandboxResourceObject(namespace string, request pool.CreateRequest, index int, labels, annotations, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox",
		"metadata": map[string]any{
			"name": fmt.Sprintf("sandbox-%s-%d", request.Name, index), "namespace": namespace,
			"labels": labels, "annotations": annotations,
		},
		"spec": spec,
	}}
}

func buildSandboxClaim(namespace string, request pool.CreateRequest, index int) *unstructured.Unstructured {
	labels := map[string]any{poolLabel: request.Name, managedLabel: managedBy}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1", "kind": "SandboxClaim",
		"metadata": map[string]any{
			"name": fmt.Sprintf("claim-%s-%d", request.Name, index), "namespace": namespace,
			"labels": labels,
		},
		"spec": map[string]any{
			"warmPoolRef": map[string]any{"name": request.WarmPoolRef},
			"additionalPodMetadata": map[string]any{
				"labels": map[string]any{poolLabel: request.Name},
			},
		},
	}}
}
