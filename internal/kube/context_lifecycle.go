package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	contextSnapshotLabel      = "context.rossoctl.io/snapshot"
	contextSourceAnnotation   = "context.rossoctl.io/source-context"
	contextSnapshotAnnotation = "context.rossoctl.io/source-snapshot"
	contextRevisionAnnotation = "context.rossoctl.io/source-revision"
	contextClaimAnnotation    = "context.rossoctl.io/source-claim"
	contextSizeAnnotation     = "context.rossoctl.io/storage-size"
	contextAccessAnnotation   = "context.rossoctl.io/access-mode"
	contextClassAnnotation    = "context.rossoctl.io/storage-class"
	contextTypeAnnotation     = "context.rossoctl.io/context-type"
	snapshotProtected         = "context.rossoctl.io/protected"
	snapshotLabelsAnnotation  = "context.rossoctl.io/snapshot-labels"
	retentionAnnotation       = "context.rossoctl.io/snapshot-retention"
	snapshotDefaultAnnotation = "snapshot.storage.kubernetes.io/is-default-class"
)

var volumeSnapshotResource = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}
var volumeSnapshotClassResource = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}

func (m *Manager) CreateContextSnapshot(ctx context.Context, namespace, name string, request contextresource.SnapshotRequest) (contextresource.Snapshot, error) {
	request.Protected = request.Protected || strings.EqualFold(request.Labels["protected"], "true")
	resource, err := m.GetContext(ctx, namespace, name)
	if err != nil {
		return contextresource.Snapshot{}, err
	}
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return contextresource.Snapshot{}, err
	}
	snapshotClass, err := m.resolveSnapshotClass(ctx, pvc, request.SnapshotClass)
	if err != nil {
		return contextresource.Snapshot{}, err
	}
	userLabels, err := json.Marshal(request.Labels)
	if err != nil {
		return contextresource.Snapshot{}, err
	}
	annotations := map[string]any{
		contextClaimAnnotation: pvc.Name, contextSizeAnnotation: resource.Storage.Size,
		contextAccessAnnotation: resource.Storage.AccessMode, contextClassAnnotation: resource.Storage.StorageClass,
		contextTypeAnnotation: resource.Type, contextRevisionAnnotation: resource.CurrentRevision,
		snapshotProtected: strconv.FormatBool(request.Protected), snapshotLabelsAnnotation: string(userLabels),
	}
	snapshot := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot",
		"metadata": map[string]any{
			"name": contextSnapshotName(name, request.Name), "namespace": namespace,
			"labels":      map[string]any{managedLabel: managedBy, contextLabel: name, contextSnapshotLabel: request.Name, contextTypeLabel: resource.Type},
			"annotations": annotations,
		},
		"spec": map[string]any{"volumeSnapshotClassName": snapshotClass, "source": map[string]any{"persistentVolumeClaimName": pvc.Name}},
	}}
	created, err := m.dynamic.Resource(volumeSnapshotResource).Namespace(namespace).Create(ctx, snapshot, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return contextresource.Snapshot{}, contextresource.ErrAlreadyExists
	}
	if apierrors.IsNotFound(err) {
		return contextresource.Snapshot{}, contextresource.ErrUnsupported
	}
	if err != nil {
		return contextresource.Snapshot{}, fmt.Errorf("create context snapshot: %w", err)
	}
	return snapshotFromObject(created), nil
}

func (m *Manager) ListContextSnapshots(ctx context.Context, namespace, name string) ([]contextresource.Snapshot, error) {
	if _, err := m.GetContext(ctx, namespace, name); err != nil {
		return nil, err
	}
	items, err := m.dynamic.Resource(volumeSnapshotResource).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: managedLabel + "=" + managedBy + "," + contextLabel + "=" + name})
	if apierrors.IsNotFound(err) {
		return nil, contextresource.ErrUnsupported
	}
	if err != nil {
		return nil, fmt.Errorf("list context snapshots: %w", err)
	}
	result := make([]contextresource.Snapshot, 0, len(items.Items))
	for i := range items.Items {
		result = append(result, snapshotFromObject(&items.Items[i]))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (m *Manager) GetContextSnapshot(ctx context.Context, namespace, name, snapshotName string) (contextresource.Snapshot, error) {
	item, err := m.dynamic.Resource(volumeSnapshotResource).Namespace(namespace).Get(ctx, contextSnapshotName(name, snapshotName), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return contextresource.Snapshot{}, contextresource.ErrNotFound
	}
	if err != nil {
		return contextresource.Snapshot{}, fmt.Errorf("get context snapshot: %w", err)
	}
	labels := item.GetLabels()
	if labels[managedLabel] != managedBy || labels[contextLabel] != name || labels[contextSnapshotLabel] != snapshotName {
		return contextresource.Snapshot{}, contextresource.ErrNotFound
	}
	return snapshotFromObject(item), nil
}

func (m *Manager) CloneContextSnapshot(ctx context.Context, namespace, sourceName string, request contextresource.CloneRequest) (contextresource.Resource, error) {
	snapshot, err := m.GetContextSnapshot(ctx, namespace, sourceName, request.Snapshot)
	if err != nil {
		return contextresource.Resource{}, err
	}
	if snapshot.Status != "ready" {
		return contextresource.Resource{}, fmt.Errorf("%w: snapshot %q is not ready", contextresource.ErrInvalid, request.Snapshot)
	}
	object, err := m.dynamic.Resource(volumeSnapshotResource).Namespace(namespace).Get(ctx, snapshot.SnapshotName, metav1.GetOptions{})
	if err != nil {
		return contextresource.Resource{}, fmt.Errorf("get source snapshot: %w", err)
	}
	annotations := object.GetAnnotations()
	create := contextresource.CreateRequest{
		Name: request.Name, Namespace: namespace, Type: annotations[contextTypeAnnotation], Owner: request.Owner,
		Storage: contextresource.Storage{Backend: "pvc", Size: annotations[contextSizeAnnotation], AccessMode: annotations[contextAccessAnnotation], StorageClass: annotations[contextClassAnnotation]},
	}
	if create.Type == "" || create.Storage.Size == "" || create.Storage.AccessMode == "" {
		return contextresource.Resource{}, fmt.Errorf("%w: snapshot is missing source metadata", contextresource.ErrInvalid)
	}
	pvc := buildContextPVC(create)
	apiGroup := "snapshot.storage.k8s.io"
	pvc.Spec.DataSource = &corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: "VolumeSnapshot", Name: snapshot.SnapshotName}
	pvc.Annotations[contextSourceAnnotation] = sourceName
	pvc.Annotations[contextSnapshotAnnotation] = request.Snapshot
	pvc.Annotations[contextRevisionAnnotation] = snapshot.Revision
	if snapshot.Revision != "" {
		source := contextresource.SourceReference{Context: sourceName, Type: create.Type, Revision: snapshot.Revision}
		revision := contextresource.Revision{ID: snapshot.Revision, CreatedAt: time.Now().UTC(), Operation: "clone", Producer: "context-service", Sources: []contextresource.SourceReference{source}}
		encoded, _ := json.Marshal([]contextresource.Revision{revision})
		pvc.Annotations[currentRevisionAnnotation] = snapshot.Revision
		pvc.Annotations[revisionsAnnotation] = string(encoded)
	}
	if _, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
		return contextresource.Resource{}, contextresource.ErrAlreadyExists
	} else if err != nil {
		return contextresource.Resource{}, fmt.Errorf("create cloned context PVC: %w", err)
	}
	return m.GetContext(ctx, namespace, request.Name)
}

func (m *Manager) RestoreContextSnapshot(context.Context, string, string, contextresource.RestoreRequest) (contextresource.Resource, error) {
	return contextresource.Resource{}, fmt.Errorf("%w: CSI cannot replace an existing PVC atomically; clone the snapshot into a new context", contextresource.ErrUnsupported)
}

func (m *Manager) SetContextRetention(ctx context.Context, namespace, name string, request contextresource.RetentionRequest) (contextresource.Resource, error) {
	if request.KeepLast < 0 {
		return contextresource.Resource{}, fmt.Errorf("%w: keepLast cannot be negative", contextresource.ErrInvalid)
	}
	if request.MaxAge != "" {
		if duration, err := time.ParseDuration(request.MaxAge); err != nil || duration < 0 {
			return contextresource.Resource{}, fmt.Errorf("%w: maxAge must be a non-negative duration", contextresource.ErrInvalid)
		} else if duration == 0 {
			request.MaxAge = ""
		}
	}
	return m.updateAccessMetadata(ctx, namespace, name, func(pvc *corev1.PersistentVolumeClaim) error {
		return writeAnnotation(pvc, retentionAnnotation, request)
	})
}

func (m *Manager) GarbageCollectContextSnapshots(ctx context.Context, namespace, name string, dryRun bool) (contextresource.GarbageCollection, error) {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return contextresource.GarbageCollection{}, err
	}
	var policy contextresource.RetentionRequest
	_ = json.Unmarshal([]byte(pvc.Annotations[retentionAnnotation]), &policy)
	items, err := m.ListContextSnapshots(ctx, namespace, name)
	if err != nil {
		return contextresource.GarbageCollection{}, err
	}
	result := contextresource.GarbageCollection{DryRun: dryRun}
	if policy.KeepLast == 0 && policy.MaxAge == "" {
		result.Kept = items
		return result, nil
	}
	newest := append([]contextresource.Snapshot(nil), items...)
	sort.Slice(newest, func(i, j int) bool { return newest[i].CreatedAt.After(newest[j].CreatedAt) })
	keep := map[string]bool{}
	for i := 0; i < len(newest) && i < policy.KeepLast; i++ {
		keep[newest[i].Name] = true
	}
	var cutoff time.Time
	if policy.MaxAge != "" {
		duration, _ := time.ParseDuration(policy.MaxAge)
		cutoff = time.Now().UTC().Add(-duration)
	}
	for _, item := range items {
		referenced, err := m.snapshotReferenced(ctx, namespace, name, item)
		if err != nil {
			return contextresource.GarbageCollection{}, err
		}
		if item.Protected || keep[item.Name] || (!cutoff.IsZero() && !item.CreatedAt.Before(cutoff)) || referenced {
			result.Kept = append(result.Kept, item)
		} else {
			result.Deleted = append(result.Deleted, item)
		}
	}
	if !dryRun {
		for _, item := range result.Deleted {
			if err := m.dynamic.Resource(volumeSnapshotResource).Namespace(namespace).Delete(ctx, item.SnapshotName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return contextresource.GarbageCollection{}, fmt.Errorf("delete snapshot %s: %w", item.Name, err)
			}
		}
	}
	return result, nil
}

func (m *Manager) ContextLifecycleCapabilities(ctx context.Context, namespace, name string) (contextresource.LifecycleCapabilities, error) {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return contextresource.LifecycleCapabilities{}, err
	}
	driver, err := m.provisionerForPVC(ctx, pvc)
	if err != nil {
		return contextresource.LifecycleCapabilities{Reason: err.Error()}, nil
	}
	class, err := m.resolveSnapshotClass(ctx, pvc, "")
	if err != nil {
		return contextresource.LifecycleCapabilities{Driver: driver, Reason: err.Error()}, nil
	}
	return contextresource.LifecycleCapabilities{Snapshots: true, Clones: true, Restore: false, Driver: driver, Class: class, Reason: "restore requires cloning into a new context"}, nil
}

func (m *Manager) snapshotReferenced(ctx context.Context, namespace, source string, snapshot contextresource.Snapshot) (bool, error) {
	pvcs, err := m.core.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: managedLabel + "=" + managedBy})
	if err != nil {
		return false, err
	}
	for i := range pvcs.Items {
		annotations := pvcs.Items[i].Annotations
		if annotations[contextSourceAnnotation] == source && (annotations[contextSnapshotAnnotation] == snapshot.Name || (snapshot.Revision != "" && annotations[contextRevisionAnnotation] == snapshot.Revision)) {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) resolveSnapshotClass(ctx context.Context, pvc *corev1.PersistentVolumeClaim, requested string) (string, error) {
	provisioner, err := m.provisionerForPVC(ctx, pvc)
	if err != nil {
		return "", err
	}
	classes, err := m.dynamic.Resource(volumeSnapshotClassResource).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return "", contextresource.ErrUnsupported
	}
	if err != nil {
		return "", fmt.Errorf("list VolumeSnapshotClasses: %w", err)
	}
	var matches []*unstructured.Unstructured
	for i := range classes.Items {
		driver, _, _ := unstructured.NestedString(classes.Items[i].Object, "driver")
		if driver == provisioner {
			matches = append(matches, &classes.Items[i])
		}
	}
	if requested != "" {
		for _, class := range matches {
			if class.GetName() == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("%w: snapshot class %q does not match storage driver %q", contextresource.ErrInvalid, requested, provisioner)
	}
	for _, class := range matches {
		if class.GetAnnotations()[snapshotDefaultAnnotation] == "true" {
			return class.GetName(), nil
		}
	}
	if len(matches) == 1 {
		return matches[0].GetName(), nil
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%w: no VolumeSnapshotClass for storage driver %q", contextresource.ErrUnsupported, provisioner)
	}
	return "", fmt.Errorf("%w: multiple VolumeSnapshotClasses match storage driver %q; specify snapshotClass", contextresource.ErrInvalid, provisioner)
}

func (m *Manager) provisionerForPVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) (string, error) {
	className := ""
	if pvc.Spec.StorageClassName != nil {
		className = *pvc.Spec.StorageClassName
	}
	if className == "" {
		classes, err := m.core.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", fmt.Errorf("list storage classes: %w", err)
		}
		for i := range classes.Items {
			annotations := classes.Items[i].Annotations
			if annotations[defaultStorageClassAnnotation] == "true" || annotations[betaDefaultStorageClassAnnotation] == "true" {
				className = classes.Items[i].Name
				break
			}
		}
	}
	if className == "" {
		return "", fmt.Errorf("%w: context PVC has no storage class", contextresource.ErrInvalid)
	}
	class, err := m.core.StorageV1().StorageClasses().Get(ctx, className, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Errorf("%w: storage class %q not found", contextresource.ErrInvalid, className)
	}
	if err != nil {
		return "", fmt.Errorf("get storage class: %w", err)
	}
	return class.Provisioner, nil
}

func snapshotFromObject(snapshot *unstructured.Unstructured) contextresource.Snapshot {
	labels, annotations := snapshot.GetLabels(), snapshot.GetAnnotations()
	ready, _, _ := unstructured.NestedBool(snapshot.Object, "status", "readyToUse")
	status := "creating"
	if ready {
		status = "ready"
	}
	errorMessage := ""
	if message, found, _ := unstructured.NestedString(snapshot.Object, "status", "error", "message"); found && message != "" {
		status, errorMessage = "failed", message
	}
	class, _, _ := unstructured.NestedString(snapshot.Object, "spec", "volumeSnapshotClassName")
	var userLabels map[string]string
	_ = json.Unmarshal([]byte(annotations[snapshotLabelsAnnotation]), &userLabels)
	return contextresource.Snapshot{
		Name: labels[contextSnapshotLabel], ContextName: labels[contextLabel], Namespace: snapshot.GetNamespace(),
		Revision: annotations[contextRevisionAnnotation], Status: status, SnapshotName: snapshot.GetName(), SnapshotClass: class,
		SourceClaimName: annotations[contextClaimAnnotation], CreatedAt: snapshot.GetCreationTimestamp().Time,
		Protected: annotations[snapshotProtected] == "true", Labels: userLabels, Error: errorMessage,
	}
}

func contextSnapshotName(contextName, snapshot string) string {
	base := "context-" + contextName + "-" + snapshot
	if len(base) <= 63 {
		return base
	}
	digest := sha256.Sum256([]byte(base))
	return strings.TrimRight(base[:54], "-") + "-" + hex.EncodeToString(digest[:])[:8]
}
