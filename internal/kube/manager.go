package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/storageclass"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
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
	managedLabel              = "app.kubernetes.io/managed-by"
	managedBy                 = "context-service"
	contextLabel              = "context.rossoctl.io/name"
	contextTypeLabel          = "context.rossoctl.io/type"
	currentRevisionAnnotation = "context.rossoctl.io/current-revision"
	revisionsAnnotation       = "context.rossoctl.io/revisions"
)

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
	var revisions []contextresource.Revision
	if encoded := pvc.Annotations[revisionsAnnotation]; encoded != "" {
		_ = json.Unmarshal([]byte(encoded), &revisions)
	}
	return contextresource.Resource{
		Name: pvc.Labels[contextLabel], Namespace: pvc.Namespace, Type: pvc.Labels[contextTypeLabel], Status: status,
		Storage: contextresource.Storage{
			Backend: "pvc", Size: pvc.Spec.Resources.Requests.Storage().String(),
			AccessMode: string(pvc.Spec.AccessModes[0]), StorageClass: storageClass,
		},
		Attachment:      contextresource.Attachment{Kind: "pvc", ClaimName: pvc.Name},
		CurrentRevision: pvc.Annotations[currentRevisionAnnotation], Revisions: revisions,
	}
}

func (m *Manager) PublishContextRevision(ctx context.Context, namespace, name string, revision contextresource.Revision) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pvc, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, contextPVCName(name), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return contextresource.ErrNotFound
		}
		if err != nil {
			return err
		}
		if pvc.Labels[managedLabel] != managedBy || pvc.Labels[contextLabel] != name {
			return contextresource.ErrNotFound
		}
		var revisions []contextresource.Revision
		if encoded := pvc.Annotations[revisionsAnnotation]; encoded != "" {
			if err := json.Unmarshal([]byte(encoded), &revisions); err != nil {
				return fmt.Errorf("read context revision history: %w", err)
			}
		}
		if pvc.Annotations[currentRevisionAnnotation] != revision.ID {
			revisions = append(revisions, revision)
		}
		encoded, err := json.Marshal(revisions)
		if err != nil {
			return err
		}
		if pvc.Annotations == nil {
			pvc.Annotations = map[string]string{}
		}
		pvc.Annotations[currentRevisionAnnotation] = revision.ID
		pvc.Annotations[revisionsAnnotation] = string(encoded)
		updated, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, pvc, metav1.UpdateOptions{})
		if err == nil {
			result = resourceFromContextPVC(updated)
		}
		return err
	})
	if errors.Is(err, contextresource.ErrNotFound) {
		return contextresource.Resource{}, err
	}
	if err != nil {
		return contextresource.Resource{}, fmt.Errorf("publish context revision: %w", err)
	}
	return result, nil
}

func (m *Manager) DeleteContext(ctx context.Context, namespace, name string) error {
	return m.deleteContext(ctx, namespace, name, false)
}

func (m *Manager) deleteContext(ctx context.Context, namespace, name string, force bool) error {
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
	if !force {
		consumers, err := m.ListContextConsumers(ctx, namespace, name)
		if err != nil {
			return err
		}
		if len(consumers) > 0 {
			names := make([]string, 0, len(consumers))
			for _, consumer := range consumers {
				names = append(names, consumer.Kind+"/"+consumer.Name)
			}
			return fmt.Errorf("%w: %s is used by %s; detach consumers or use force deletion", contextresource.ErrInUse, name, strings.Join(names, ", "))
		}
	}
	if err := m.core.CoreV1().ConfigMaps(namespace).Delete(ctx, queryIndexName(name), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete context query index: %w", err)
	}
	if err := m.core.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete context PVC: %w", err)
	}
	return nil
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
