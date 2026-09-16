package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const (
	grantsAnnotation    = "context.rossoctl.io/access-grants"
	consumersAnnotation = "context.rossoctl.io/consumers"
	auditAnnotation     = "context.rossoctl.io/audit-events"
	maxAuditEvents      = 100
)

var administratorSubject = contextresource.Subject{Kind: "service", Name: "context-service-admin"}

func normalizeOwner(subject contextresource.Subject) contextresource.Subject {
	if subject.Kind == "" && subject.Name == "" {
		return contextresource.Subject{Kind: "user", Name: "anonymous"}
	}
	return subject
}

func ownerGrant(subject contextresource.Subject, createdAt time.Time) contextresource.Grant {
	return contextresource.Grant{Subject: normalizeOwner(subject), CreatedAt: createdAt, Permissions: []contextresource.Permission{
		contextresource.PermissionRead, contextresource.PermissionWrite, contextresource.PermissionAttach,
		contextresource.PermissionDerive, contextresource.PermissionAdminister,
	}}
}

func (m *Manager) ListAccessibleContexts(ctx context.Context, namespace string, subject contextresource.Subject) ([]contextresource.Resource, error) {
	pvcs, err := m.core.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: managedLabel + "=" + managedBy})
	if err != nil {
		return nil, fmt.Errorf("list context PVCs: %w", err)
	}
	items := make([]contextresource.Resource, 0, len(pvcs.Items))
	for index := range pvcs.Items {
		pvc := &pvcs.Items[index]
		if pvc.Labels[contextLabel] == "" || !hasAnyAccess(readGrants(pvc), subject) {
			continue
		}
		resource := resourceFromContextPVC(pvc)
		resource.EffectiveAccess = effectivePermissions(readGrants(pvc), subject)
		consumers, err := m.ListContextConsumers(ctx, namespace, resource.Name)
		if err != nil {
			return nil, err
		}
		resource.Consumers = consumers
		items = append(items, resource)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (m *Manager) AccessContext(ctx context.Context, namespace, name string, subject contextresource.Subject, permission contextresource.Permission) (contextresource.Resource, error) {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return contextresource.Resource{}, err
	}
	permissions := effectivePermissions(readGrants(pvc), subject)
	if !containsPermission(permissions, permission) {
		return contextresource.Resource{}, contextresource.ErrNotFound
	}
	result := resourceFromContextPVC(pvc)
	result.EffectiveAccess = permissions
	consumers, err := m.ListContextConsumers(ctx, namespace, name)
	if err != nil {
		return contextresource.Resource{}, err
	}
	result.Consumers = consumers
	return result, nil
}

func (m *Manager) ListContextGrants(ctx context.Context, namespace, name string) ([]contextresource.Grant, error) {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	return readGrants(pvc), nil
}

func (m *Manager) SetContextGrant(ctx context.Context, namespace, name string, grant contextresource.Grant, actor contextresource.Subject) (contextresource.Resource, error) {
	return m.updateAccessMetadata(ctx, namespace, name, func(pvc *corev1.PersistentVolumeClaim) error {
		grants := readGrants(pvc)
		grant.CreatedAt = time.Now().UTC()
		replaced := false
		for index := range grants {
			if sameSubject(grants[index].Subject, grant.Subject) {
				grants[index] = grant
				replaced = true
				break
			}
		}
		if !replaced {
			grants = append(grants, grant)
		}
		sort.Slice(grants, func(i, j int) bool { return subjectKey(grants[i].Subject) < subjectKey(grants[j].Subject) })
		if err := writeAnnotation(pvc, grantsAnnotation, grants); err != nil {
			return err
		}
		appendAudit(pvc, contextresource.AuditEvent{Time: time.Now().UTC(), Action: "grant.set", Subject: actor, Target: &grant.Subject})
		return nil
	})
}

func (m *Manager) RevokeContextGrant(ctx context.Context, namespace, name string, target, actor contextresource.Subject) (contextresource.Resource, error) {
	return m.updateAccessMetadata(ctx, namespace, name, func(pvc *corev1.PersistentVolumeClaim) error {
		grants := readGrants(pvc)
		filtered := grants[:0]
		for _, grant := range grants {
			if !sameSubject(grant.Subject, target) {
				filtered = append(filtered, grant)
			}
		}
		if len(filtered) == len(grants) {
			return contextresource.ErrNotFound
		}
		if err := writeAnnotation(pvc, grantsAnnotation, filtered); err != nil {
			return err
		}
		appendAudit(pvc, contextresource.AuditEvent{Time: time.Now().UTC(), Action: "grant.revoked", Subject: actor, Target: &target})
		return nil
	})
}

func (m *Manager) ListContextConsumers(ctx context.Context, namespace, name string) ([]contextresource.Consumer, error) {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	consumers := readConsumers(pvc)
	pods, err := m.core.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list context consumers: %w", err)
	}
	for index := range pods.Items {
		pod := &pods.Items[index]
		if podUsesClaim(pod, pvc.Name) {
			consumers = append(consumers, contextresource.Consumer{
				Subject: contextresource.Subject{Kind: "service", Name: namespace + "/" + pod.Spec.ServiceAccountName},
				Kind:    "pod", Name: pod.Name, AccessMode: podClaimMode(pod, pvc.Name),
				Active: pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil, Desired: true,
			})
		}
	}
	sort.Slice(consumers, func(i, j int) bool {
		return consumers[i].Kind+"/"+consumers[i].Name < consumers[j].Kind+"/"+consumers[j].Name
	})
	return consumers, nil
}

func (m *Manager) SetContextConsumer(ctx context.Context, namespace, name string, consumer contextresource.Consumer, attached bool, actor contextresource.Subject) (contextresource.Resource, error) {
	return m.updateAccessMetadata(ctx, namespace, name, func(pvc *corev1.PersistentVolumeClaim) error {
		consumers := readConsumers(pvc)
		filtered := consumers[:0]
		for _, existing := range consumers {
			if !(sameSubject(existing.Subject, consumer.Subject) && existing.Kind == consumer.Kind && existing.Name == consumer.Name) {
				filtered = append(filtered, existing)
			}
		}
		if attached {
			consumer.Desired = true
			consumer.Since = time.Now().UTC()
			filtered = append(filtered, consumer)
		}
		if err := writeAnnotation(pvc, consumersAnnotation, filtered); err != nil {
			return err
		}
		action := "consumer.detached"
		if attached {
			action = "consumer.attached"
		}
		appendAudit(pvc, contextresource.AuditEvent{Time: time.Now().UTC(), Action: action, Subject: actor, Target: &consumer.Subject})
		return nil
	})
}

func (m *Manager) ListContextAudit(ctx context.Context, namespace, name string) ([]contextresource.AuditEvent, error) {
	pvc, err := m.contextPVC(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	var events []contextresource.AuditEvent
	_ = json.Unmarshal([]byte(pvc.Annotations[auditAnnotation]), &events)
	return events, nil
}

func (m *Manager) ForceDeleteContext(ctx context.Context, namespace, name string) error {
	return m.deleteContext(ctx, namespace, name, true)
}

func (m *Manager) contextPVC(ctx context.Context, namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	pvc, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, contextPVCName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, contextresource.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if pvc.Labels[managedLabel] != managedBy || pvc.Labels[contextLabel] != name {
		return nil, contextresource.ErrNotFound
	}
	return pvc, nil
}

func (m *Manager) updateAccessMetadata(ctx context.Context, namespace, name string, mutate func(*corev1.PersistentVolumeClaim) error) (contextresource.Resource, error) {
	var result contextresource.Resource
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pvc, err := m.contextPVC(ctx, namespace, name)
		if err != nil {
			return err
		}
		if err := mutate(pvc); err != nil {
			return err
		}
		updated, err := m.core.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, pvc, metav1.UpdateOptions{})
		if err == nil {
			result = resourceFromContextPVC(updated)
		}
		return err
	})
	return result, err
}

func readGrants(pvc *corev1.PersistentVolumeClaim) []contextresource.Grant {
	encoded, present := pvc.Annotations[grantsAnnotation]
	if !present {
		// Contexts created before access grants were introduced were implicitly
		// owned by the unauthenticated user. Preserve that behavior without
		// weakening contexts that explicitly declare an empty grant set.
		createdAt := time.Time{}
		if !pvc.CreationTimestamp.IsZero() {
			createdAt = pvc.CreationTimestamp.Time
		}
		return []contextresource.Grant{ownerGrant(contextresource.Subject{}, createdAt)}
	}
	var grants []contextresource.Grant
	_ = json.Unmarshal([]byte(encoded), &grants)
	return grants
}

func readConsumers(pvc *corev1.PersistentVolumeClaim) []contextresource.Consumer {
	var consumers []contextresource.Consumer
	_ = json.Unmarshal([]byte(pvc.Annotations[consumersAnnotation]), &consumers)
	return consumers
}

func writeAnnotation(pvc *corev1.PersistentVolumeClaim, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if pvc.Annotations == nil {
		pvc.Annotations = map[string]string{}
	}
	pvc.Annotations[key] = string(encoded)
	return nil
}

func appendAudit(pvc *corev1.PersistentVolumeClaim, event contextresource.AuditEvent) {
	var events []contextresource.AuditEvent
	_ = json.Unmarshal([]byte(pvc.Annotations[auditAnnotation]), &events)
	events = append(events, event)
	if len(events) > maxAuditEvents {
		events = events[len(events)-maxAuditEvents:]
	}
	_ = writeAnnotation(pvc, auditAnnotation, events)
}

func effectivePermissions(grants []contextresource.Grant, subject contextresource.Subject) []contextresource.Permission {
	if sameSubject(subject, administratorSubject) {
		return ownerGrant(subject, time.Time{}).Permissions
	}
	seen := map[contextresource.Permission]bool{}
	for _, grant := range grants {
		if sameSubject(grant.Subject, subject) {
			for _, permission := range grant.Permissions {
				seen[permission] = true
			}
		}
	}
	order := ownerGrant(subject, time.Time{}).Permissions
	result := make([]contextresource.Permission, 0, len(seen))
	for _, permission := range order {
		if seen[permission] {
			result = append(result, permission)
		}
	}
	return result
}

func hasAnyAccess(grants []contextresource.Grant, subject contextresource.Subject) bool {
	return len(effectivePermissions(grants, subject)) > 0
}
func containsPermission(values []contextresource.Permission, expected contextresource.Permission) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func sameSubject(left, right contextresource.Subject) bool {
	return left.Kind == right.Kind && left.Name == right.Name
}
func subjectKey(subject contextresource.Subject) string { return subject.Kind + ":" + subject.Name }

func podUsesClaim(pod *corev1.Pod, claimName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claimName {
			return true
		}
	}
	return false
}

func podClaimMode(pod *corev1.Pod, claimName string) string {
	volumeNames := map[string]bool{}
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claimName {
			volumeNames[volume.Name] = true
		}
	}
	readOnly := true
	for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		for _, mount := range container.VolumeMounts {
			if volumeNames[mount.Name] && !mount.ReadOnly {
				readOnly = false
			}
		}
	}
	if readOnly {
		return "readOnly"
	}
	return "readWrite"
}
