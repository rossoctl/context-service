package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
	"github.com/rossoctl/context-service/internal/storageclass"
)

type fakeManager struct {
	created          pool.CreateRequest
	createdContext   contextresource.CreateRequest
	deniedSubject    string
	deniedPermission contextresource.Permission
	deleteErr        error
}

func (f *fakeManager) Create(_ context.Context, request pool.CreateRequest) (pool.Pool, error) {
	f.created = request
	return pool.Pool{Name: request.Name, Status: "provisioning", Replicas: request.Replicas}, nil
}
func (f *fakeManager) List(_ context.Context) ([]pool.Pool, error) {
	return []pool.Pool{{Name: "review", Status: "ready", Replicas: 2, ReadyReplicas: 2}}, nil
}
func (f *fakeManager) Get(_ context.Context, name string) (pool.Pool, error) {
	return pool.Pool{Name: name, Status: "ready"}, nil
}

func TestListPools(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/sandbox-pools", nil)
	response := httptest.NewRecorder()
	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"name":"review"`)) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
func (f *fakeManager) Delete(_ context.Context, _ string) error { return nil }
func (f *fakeManager) CreateContext(_ context.Context, request contextresource.CreateRequest) (contextresource.Resource, error) {
	f.createdContext = request
	return contextresource.Resource{Name: request.Name, Namespace: request.Namespace, Type: request.Type, Status: "provisioning"}, nil
}
func (f *fakeManager) ListContexts(_ context.Context, namespace string) ([]contextresource.Resource, error) {
	return []contextresource.Resource{{Name: "research", Namespace: namespace, Type: "workspace", Status: "ready"}}, nil
}
func (f *fakeManager) GetContext(_ context.Context, namespace, name string) (contextresource.Resource, error) {
	return contextresource.Resource{Name: name, Namespace: namespace, Type: "workspace", Status: "ready"}, nil
}
func (f *fakeManager) DeleteContext(_ context.Context, _, _ string) error { return f.deleteErr }
func (f *fakeManager) PublishContextRevision(_ context.Context, namespace, name string, revision contextresource.Revision) (contextresource.Resource, error) {
	return contextresource.Resource{Name: name, Namespace: namespace, CurrentRevision: revision.ID, Revisions: []contextresource.Revision{revision}}, nil
}
func (f *fakeManager) ListAccessibleContexts(ctx context.Context, namespace string, subject contextresource.Subject) ([]contextresource.Resource, error) {
	if subject.Name == f.deniedSubject {
		return []contextresource.Resource{}, nil
	}
	return f.ListContexts(ctx, namespace)
}
func (f *fakeManager) AccessContext(ctx context.Context, namespace, name string, subject contextresource.Subject, permission contextresource.Permission) (contextresource.Resource, error) {
	if subject.Name == f.deniedSubject || permission == f.deniedPermission {
		return contextresource.Resource{}, contextresource.ErrNotFound
	}
	return f.GetContext(ctx, namespace, name)
}

func TestReadOnlySubjectCannotRegisterReadWriteConsumer(t *testing.T) {
	body := bytes.NewBufferString(`{"kind":"agent","name":"reader","accessMode":"readWrite"}`)
	request := httptest.NewRequest(http.MethodPut, "/v1/namespaces/team1/contexts/research/consumers", body)
	request.Header.Set("X-Context-Subject", "agent:reader")
	response := httptest.NewRecorder()
	NewHandler(&fakeManager{deniedPermission: contextresource.PermissionWrite}).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
func (f *fakeManager) ListContextGrants(_ context.Context, _, _ string) ([]contextresource.Grant, error) {
	return nil, nil
}
func (f *fakeManager) SetContextGrant(_ context.Context, namespace, name string, _ contextresource.Grant, _ contextresource.Subject) (contextresource.Resource, error) {
	return f.GetContext(context.Background(), namespace, name)
}
func (f *fakeManager) RevokeContextGrant(_ context.Context, namespace, name string, _, _ contextresource.Subject) (contextresource.Resource, error) {
	return f.GetContext(context.Background(), namespace, name)
}
func (f *fakeManager) ListContextConsumers(_ context.Context, _, _ string) ([]contextresource.Consumer, error) {
	return nil, nil
}
func (f *fakeManager) SetContextConsumer(_ context.Context, namespace, name string, _ contextresource.Consumer, _ bool, _ contextresource.Subject) (contextresource.Resource, error) {
	return f.GetContext(context.Background(), namespace, name)
}
func (f *fakeManager) ListContextAudit(_ context.Context, _, _ string) ([]contextresource.AuditEvent, error) {
	return nil, nil
}
func (f *fakeManager) ForceDeleteContext(ctx context.Context, namespace, name string) error {
	return f.DeleteContext(ctx, namespace, name)
}
func (f *fakeManager) ListStorageClasses(_ context.Context) ([]storageclass.Resource, error) {
	return []storageclass.Resource{{Name: "fast", Default: true, Provisioner: "example.csi.io", VolumeBindingMode: "WaitForFirstConsumer", ReclaimPolicy: "Delete", AllowVolumeExpansion: true}}, nil
}

func TestListStorageClasses(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/storage-classes", nil)
	response := httptest.NewRecorder()

	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{`"name":"fast"`, `"default":true`, `"provisioner":"example.csi.io"`, `"volumeBindingMode":"WaitForFirstConsumer"`, `"allowVolumeExpansion":true`} {
		if !bytes.Contains(response.Body.Bytes(), []byte(expected)) {
			t.Errorf("response missing %s: %s", expected, response.Body.String())
		}
	}
}

func TestCreateWorkspaceContext(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"research","namespace":"team1","type":"workspace","storage":{"backend":"pvc","size":"10Gi","accessMode":"ReadWriteMany","storageClass":"ibm-scale-csi"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/contexts", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.createdContext.Namespace != "team1" || manager.createdContext.Type != "workspace" || manager.createdContext.Storage.AccessMode != "ReadWriteMany" {
		t.Fatalf("unexpected context request: %#v", manager.createdContext)
	}
}

func TestCreateContextRecordsAuthenticatedOwner(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"research","namespace":"team1","type":"state","storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/contexts", bytes.NewReader(body))
	request.Header.Set("X-Context-Subject", "agent:researcher")
	response := httptest.NewRecorder()
	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.createdContext.Owner != (contextresource.Subject{Kind: "agent", Name: "researcher"}) {
		t.Fatalf("owner = %+v", manager.createdContext.Owner)
	}
}

func TestUnauthorizedContextIsHiddenFromListAndGet(t *testing.T) {
	manager := &fakeManager{deniedSubject: "outsider"}
	for _, path := range []string{"/v1/namespaces/team1/contexts", "/v1/namespaces/team1/contexts/research"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-Context-Subject", "agent:outsider")
		response := httptest.NewRecorder()
		NewHandler(manager).ServeHTTP(response, request)
		if path == "/v1/namespaces/team1/contexts" {
			if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"items":[]`)) {
				t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
			}
		} else if response.Code != http.StatusNotFound {
			t.Fatalf("get status = %d, body = %s", response.Code, response.Body.String())
		}
	}
}

func TestDeleteInUseReturnsConflict(t *testing.T) {
	request := httptest.NewRequest(http.MethodDelete, "/v1/namespaces/team1/contexts/research", nil)
	response := httptest.NewRecorder()
	NewHandler(&fakeManager{deleteErr: contextresource.ErrInUse}).ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateContextAcceptsSupportedTypes(t *testing.T) {
	for _, contextType := range []string{"workspace", "state", "memory", "knowledge", "artifacts"} {
		t.Run(contextType, func(t *testing.T) {
			manager := &fakeManager{}
			body := []byte(`{"name":"research","namespace":"team1","type":"` + contextType + `","storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce"}}`)
			request := httptest.NewRequest(http.MethodPost, "/v1/contexts", bytes.NewReader(body))
			response := httptest.NewRecorder()
			NewHandler(manager).ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if manager.createdContext.Type != contextType {
				t.Fatalf("type = %q, want %q", manager.createdContext.Type, contextType)
			}
		})
	}
}

func TestCreateContextRejectsUnknownType(t *testing.T) {
	body := []byte(`{"name":"session","namespace":"team1","type":"session","storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/contexts", bytes.NewReader(body))
	response := httptest.NewRecorder()
	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestListContexts(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/namespaces/team1/contexts", nil)
	response := httptest.NewRecorder()
	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"name":"research"`)) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestPublishContextRevision(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	body := []byte(`{"id":"` + digest + `","createdAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","operation":"sync","producer":"contextctl"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/namespaces/team1/contexts/research/revisions", bytes.NewReader(body))
	response := httptest.NewRecorder()
	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(digest)) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreatePool(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"bugstone-1","replicas":3,"workspace":{"size":"5Gi","accessMode":"ReadWriteMany","storageClass":"ibm-scale-csi"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.created.Name != "bugstone-1" || manager.created.Replicas != 3 {
		t.Fatalf("unexpected create request: %#v", manager.created)
	}
}

func TestCreateAcceptsDedicatedRWO(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"dedicated","replicas":3,"workspace":{"size":"1Gi","accessMode":"ReadWriteOnce"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.created.Replicas != 3 || manager.created.Workspace.AccessMode != "ReadWriteOnce" {
		t.Fatalf("unexpected create request: %#v", manager.created)
	}
}

func TestCreateAcceptsSandboxProfile(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"developer","replicas":1,"sandboxProfile":"python-tools","workspace":{"size":"1Gi","accessMode":"ReadWriteOnce"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.created.SandboxProfile != "python-tools" {
		t.Fatalf("sandbox profile = %q", manager.created.SandboxProfile)
	}
}

func TestCreateRejectsSandboxProfileWithWarmPool(t *testing.T) {
	body := []byte(`{"name":"bad","replicas":1,"sandboxProfile":"python-tools","warmPoolRef":"python-warm","workspace":{}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateAcceptsExistingReadOnlyClaim(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"readers","replicas":3,"workspace":{"claimName":"prepared-workspace","readOnly":true}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.created.Workspace.ClaimName != "prepared-workspace" || manager.created.Workspace.ReadOnly == nil || !*manager.created.Workspace.ReadOnly {
		t.Fatalf("unexpected create request: %#v", manager.created)
	}
}

func TestCreateAcceptsExistingReadWriteClaim(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"writer","replicas":1,"workspace":{"claimName":"prepared-workspace","readOnly":false}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.created.Workspace.ReadOnly == nil || *manager.created.Workspace.ReadOnly {
		t.Fatalf("unexpected create request: %#v", manager.created)
	}
}

func TestCreateRejectsClaimWithoutAccessIntent(t *testing.T) {
	body := []byte(`{"name":"ambiguous","replicas":1,"workspace":{"claimName":"prepared-workspace"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateRejectsNewReadOnlyWorkspace(t *testing.T) {
	body := []byte(`{"name":"bad","replicas":1,"workspace":{"size":"1Gi","accessMode":"ReadWriteMany","readOnly":true}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateAcceptsWarmPoolClaims(t *testing.T) {
	manager := &fakeManager{}
	body := []byte(`{"name":"fast-run","replicas":3,"warmPoolRef":"research-agents","workspace":{}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(manager).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.created.WarmPoolRef != "research-agents" || manager.created.Replicas != 3 {
		t.Fatalf("unexpected create request: %#v", manager.created)
	}
}

func TestCreateRejectsWarmPoolWithWorkspace(t *testing.T) {
	body := []byte(`{"name":"bad","replicas":1,"warmPoolRef":"research-agents","workspace":{"size":"1Gi","accessMode":"ReadWriteOnce"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
