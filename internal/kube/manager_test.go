package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func selfPod(imageID string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "context-service-0", Namespace: "context-service"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "sidecar", ImageID: "docker.io/library/other@sha256:bbbb"},
			{Name: "context-service", ImageID: imageID},
		}},
	}
}

func TestResolveUploadImageWaitsForKubeletDigest(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	gets := 0
	client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 3 {
			return true, selfPod(""), nil
		}
		return true, selfPod("docker-pullable://ghcr.io/rossoctl/context-service@sha256:aaaa"), nil
	})

	image, err := resolveUploadImage(context.Background(), client, "context-service", "context-service-0", time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if image != "ghcr.io/rossoctl/context-service@sha256:aaaa" {
		t.Fatalf("image = %q", image)
	}
	if gets != 3 {
		t.Fatalf("pod gets = %d, want 3", gets)
	}
}

func TestResolveUploadImageTimesOut(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(selfPod(""))

	_, err := resolveUploadImage(context.Background(), client, "context-service", "context-service-0", time.Millisecond, 20*time.Millisecond)
	if !errors.Is(err, errImageDigestUnavailable) {
		t.Fatalf("error = %v, want %v", err, errImageDigestUnavailable)
	}
}

func TestResolveUploadImageReturnsAPIErrorImmediately(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	gets := 0
	client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		return true, nil, errors.New("forbidden")
	})

	_, err := resolveUploadImage(context.Background(), client, "context-service", "context-service-0", time.Millisecond, time.Second)
	if err == nil || errors.Is(err, errImageDigestUnavailable) {
		t.Fatalf("error = %v, want API error", err)
	}
	if gets != 1 {
		t.Fatalf("pod gets = %d, want 1", gets)
	}
}

func TestResolveUploadImageHonorsCancellation(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(selfPod(""))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolveUploadImage(ctx, client, "context-service", "context-service-0", time.Millisecond, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
