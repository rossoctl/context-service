package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
	"k8s.io/client-go/util/retry"
)

const (
	contextUploadMount            = "/workspace"
	contextUploadVolume           = "context"
	contextUploadMaxExpandedBytes = int64(1 << 30)
	contextExportMaxArchiveBytes  = int64(256 << 20)
)

var errContextHelperImage = fmt.Errorf("%w: set CS_UPLOAD_IMAGE or POD_NAME to enable context uploads and exports", contextresource.ErrUnsupported)

// ExportFrozenContext streams one immutable materialized revision from a
// short-lived helper that mounts the Context PVC read-only.
func (m *Manager) ExportFrozenContext(ctx context.Context, namespace, name, revision, expectedUID string, output io.Writer) error {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return err
	}
	if expectedUID == "" || string(pvc.UID) != expectedUID || pvc.Annotations[currentRevisionAnnotation] != revision || pvc.Annotations[frozenRevisionAnnotation] != revision {
		return contextresource.ErrNotFound
	}
	if m.config.UploadImage == "" {
		return errContextHelperImage
	}
	if err := m.cleanupExpiredContextHelperPods(ctx, namespace, "exporter", 10*time.Minute); err != nil {
		return err
	}
	pod := buildContextExportPod(namespace, pvc.Name, pvc.UID, m.config.UploadImage)
	created, err := m.core.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create context export helper: %w", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = m.deleteContextUploadPod(cleanupContext, namespace, created.Name)
		}
	}()
	if err := m.waitForContextUploadPod(ctx, namespace, created.Name); err != nil {
		return err
	}
	pvc, err = m.contextPVC(ctx, namespace, name)
	if err != nil || string(pvc.UID) != expectedUID || pvc.Annotations[currentRevisionAnnotation] != revision || pvc.Annotations[frozenRevisionAnnotation] != revision {
		return contextresource.ErrNotFound
	}
	request := m.core.CoreV1().RESTClient().Post().Resource("pods").Name(created.Name).Namespace(namespace).SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: "exporter", Command: []string{"/context-service", "export-helper", contextUploadMount, revision, strconv.FormatInt(contextExportMaxArchiveBytes, 10)}, Stdout: true, Stderr: true,
	}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(m.config.RESTConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("create context export stream: %w", err)
	}
	var stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: output, Stderr: &stderr}); err != nil {
		return fmt.Errorf("stream frozen context revision: %w", err)
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = m.deleteContextUploadPod(cleanupContext, namespace, created.Name)
	cancel()
	if err != nil {
		return err
	}
	cleaned = true
	return nil
}

// UploadContext verifies and materializes a portable bundle in an existing
// Context PVC. It does not inspect or create Sandbox resources.
func (m *Manager) UploadContext(ctx context.Context, namespace, name, expectedUID string, input io.Reader) (contextresource.UploadResult, error) {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return contextresource.UploadResult{}, err
	}
	if expectedUID == "" || string(pvc.UID) != expectedUID {
		return contextresource.UploadResult{}, contextresource.ErrNotFound
	}
	unlock := m.lockContextUpload(expectedUID)
	defer unlock()
	pvc, err = m.contextPVC(ctx, namespace, name)
	if err != nil || string(pvc.UID) != expectedUID {
		return contextresource.UploadResult{}, contextresource.ErrNotFound
	}
	if pvc.Annotations[frozenRevisionAnnotation] != "" {
		return contextresource.UploadResult{}, contextresource.ErrFrozen
	}
	if m.config.UploadImage == "" {
		return contextresource.UploadResult{}, errContextHelperImage
	}
	maxExpandedBytes, err := contextUploadExpandedLimit(pvc)
	if err != nil {
		return contextresource.UploadResult{}, err
	}
	if err := m.cleanupExpiredContextUploadPods(ctx, namespace, 10*time.Minute); err != nil {
		return contextresource.UploadResult{}, err
	}
	pod := buildContextUploadPod(namespace, pvc.Name, pvc.UID, m.config.UploadImage)
	created, err := m.core.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return contextresource.UploadResult{}, fmt.Errorf("create context upload helper: %w", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = m.deleteContextUploadPod(cleanupContext, namespace, created.Name)
		}
	}()
	if err := m.waitForContextUploadPod(ctx, namespace, created.Name); err != nil {
		return contextresource.UploadResult{}, err
	}
	request := m.core.CoreV1().RESTClient().Post().
		Resource("pods").Name(created.Name).Namespace(namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "uploader",
			Command: []string{
				"/context-service", "upload-helper", contextUploadMount, created.Name,
				pvc.Labels[contextTypeLabel], strconv.FormatInt(maxExpandedBytes, 10),
			},
			Stdin: true, Stdout: true, Stderr: true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(m.config.RESTConfig, "POST", request.URL())
	if err != nil {
		return contextresource.UploadResult{}, fmt.Errorf("create context upload stream: %w", err)
	}
	var stdout, stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: input, Stdout: &stdout, Stderr: &stderr}); err != nil {
		var exitError utilexec.ExitError
		if errors.As(err, &exitError) {
			return contextresource.UploadResult{}, fmt.Errorf("%w: context bundle was rejected", contextresource.ErrInvalid)
		}
		return contextresource.UploadResult{}, fmt.Errorf("stream context bundle to upload helper: %w", err)
	}
	var result contextresource.UploadResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return contextresource.UploadResult{}, fmt.Errorf("decode context upload result: %w", err)
	}
	expectedPath := ".context-service/materialized/" + result.Revision
	if result.Revision == "" || result.WorkspacePath != expectedPath {
		return contextresource.UploadResult{}, fmt.Errorf("%w: upload helper returned invalid materialization metadata", contextresource.ErrInvalid)
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = m.deleteContextUploadPod(cleanupContext, namespace, created.Name)
	cancel()
	if err != nil {
		return contextresource.UploadResult{}, err
	}
	cleaned = true
	_, err = m.PublishContextRevision(ctx, namespace, name, contextresource.Revision{
		ID: result.Revision, CreatedAt: time.Now().UTC(), Operation: "upload",
		Producer: "context-service", Files: result.Files, Bytes: result.Bytes,
	})
	if err != nil {
		return contextresource.UploadResult{}, err
	}
	return result, nil
}

func contextUploadExpandedLimit(pvc *corev1.PersistentVolumeClaim) (int64, error) {
	requested := pvc.Spec.Resources.Requests.Storage().Value()
	if requested <= 0 {
		return 0, fmt.Errorf("%w: context PVC has no positive storage request", contextresource.ErrInvalid)
	}
	// Reserve ten percent for filesystem and Context Service metadata. The
	// import remains capped at 1 GiB even when the PVC is larger.
	limit := requested - requested/10
	if limit > contextUploadMaxExpandedBytes {
		limit = contextUploadMaxExpandedBytes
	}
	if limit <= 0 {
		return 0, fmt.Errorf("%w: context PVC is too small for uploads", contextresource.ErrInvalid)
	}
	return limit, nil
}

// FreezeContext atomically pins a Context to its current revision. It shares
// the per-storage lock with uploads so a revision cannot be frozen while an
// upload for the same PVC is still in progress.
func (m *Manager) FreezeContext(ctx context.Context, namespace, name, expectedRevision, expectedUID string) (contextresource.Resource, error) {
	if expectedRevision == "" || expectedUID == "" {
		return contextresource.Resource{}, contextresource.ErrRevisionConflict
	}
	unlock, locked := m.tryLockContextUpload(expectedUID)
	if !locked {
		return contextresource.Resource{}, contextresource.ErrUploadInProgress
	}
	defer unlock()

	var result contextresource.Resource
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pvc, err := m.contextPVC(ctx, namespace, name)
		if err != nil {
			return err
		}
		if string(pvc.UID) != expectedUID {
			return contextresource.ErrNotFound
		}
		if pvc.Annotations[currentRevisionAnnotation] != expectedRevision {
			return contextresource.ErrRevisionConflict
		}
		if frozen := pvc.Annotations[frozenRevisionAnnotation]; frozen != "" {
			if frozen == expectedRevision {
				result = resourceFromContextPVC(pvc)
				return nil
			}
			return contextresource.ErrFrozen
		}
		if pvc.Annotations == nil {
			pvc.Annotations = map[string]string{}
		}
		pvc.Annotations[frozenRevisionAnnotation] = expectedRevision
		updated, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, pvc, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		result = resourceFromContextPVC(updated)
		return nil
	})
	if err != nil {
		return contextresource.Resource{}, err
	}
	return result, nil
}

func (m *Manager) lockContextUpload(storageUID string) func() {
	lock := m.contextUploadLock(storageUID)
	lock.Lock()
	return lock.Unlock
}

func (m *Manager) tryLockContextUpload(storageUID string) (func(), bool) {
	lock := m.contextUploadLock(storageUID)
	if !lock.TryLock() {
		return nil, false
	}
	return lock.Unlock, true
}

func (m *Manager) contextUploadLock(storageUID string) *sync.Mutex {
	m.uploadMu.Lock()
	defer m.uploadMu.Unlock()
	if m.uploadLocks == nil {
		m.uploadLocks = map[string]*sync.Mutex{}
	}
	lock := m.uploadLocks[storageUID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.uploadLocks[storageUID] = lock
	}
	return lock
}

func (m *Manager) cleanupExpiredContextUploadPods(ctx context.Context, namespace string, age time.Duration) error {
	return m.cleanupExpiredContextHelperPods(ctx, namespace, "uploader", age)
}

func (m *Manager) cleanupExpiredContextHelperPods(ctx context.Context, namespace, component string, age time.Duration) error {
	pods, err := m.core.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: managedLabel + "=" + managedBy + ",context.rossoctl.io/component=" + component,
	})
	if err != nil {
		return fmt.Errorf("list stale context upload helpers: %w", err)
	}
	cutoff := time.Now().Add(-age)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.CreationTimestamp.Time.After(cutoff) {
			continue
		}
		grace := int64(0)
		if err := m.core.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &grace}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale context upload helper: %w", err)
		}
	}
	return nil
}

func buildContextExportPod(namespace, claimName string, claimUID types.UID, image string) *corev1.Pod {
	pod := buildContextUploadPod(namespace, claimName, claimUID, image)
	pod.GenerateName = "context-export-"
	pod.Labels["context.rossoctl.io/component"] = "exporter"
	pod.Spec.Containers[0].Name = "exporter"
	pod.Spec.Containers[0].VolumeMounts[0].ReadOnly = true
	pod.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly = true
	return pod
}

func (m *Manager) deleteContextUploadPod(ctx context.Context, namespace, name string) error {
	grace := int64(0)
	err := m.core.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete context upload helper: %w", err)
	}
	err = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := m.core.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return fmt.Errorf("wait for context upload helper deletion: %w", err)
	}
	return nil
}

func (m *Manager) waitForContextUploadPod(ctx context.Context, namespace, name string) error {
	err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		pod, err := m.core.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			return true, nil
		case corev1.PodFailed, corev1.PodSucceeded:
			return false, fmt.Errorf("context upload helper entered phase %s", pod.Status.Phase)
		default:
			return false, nil
		}
	})
	if err != nil {
		return fmt.Errorf("wait for context upload helper: %w", err)
	}
	return nil
}

func buildContextUploadPod(namespace, claimName string, claimUID types.UID, image string) *corev1.Pod {
	deadline := int64(300)
	nonRoot := true
	noToken := false
	user := int64(65532)
	fsGroupChangePolicy := corev1.FSGroupChangeOnRootMismatch
	seccompProfile := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "context-upload-", Namespace: namespace,
			Labels:          map[string]string{managedLabel: managedBy, "context.rossoctl.io/component": "uploader"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "PersistentVolumeClaim", Name: claimName, UID: claimUID}},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline,
			AutomountServiceAccountToken: &noToken,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &nonRoot, RunAsUser: &user, RunAsGroup: &user, FSGroup: &user,
				FSGroupChangePolicy: &fsGroupChangePolicy, SeccompProfile: seccompProfile,
			},
			Containers: []corev1.Container{{
				Name: "uploader", Image: image, Args: []string{"upload-wait"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &noToken, ReadOnlyRootFilesystem: &nonRoot,
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: contextUploadVolume, MountPath: contextUploadMount}},
			}},
			Volumes: []corev1.Volume{{
				Name:         contextUploadVolume,
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}},
			}},
		},
	}
}
