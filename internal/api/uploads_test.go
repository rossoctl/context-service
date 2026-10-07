package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
)

func issueUpload(t *testing.T, handler http.Handler) uploadCapabilityResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/namespaces/team1/contexts/research/upload-capabilities", nil)
	request.Header.Set("Authorization", "Bearer moca-secret")
	request.Header.Set("X-Context-Subject", "user:alice")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("issue status = %d, body = %s", response.Code, response.Body.String())
	}
	var capability uploadCapabilityResponse
	if err := json.Unmarshal(response.Body.Bytes(), &capability); err != nil {
		t.Fatal(err)
	}
	return capability
}

func TestContextUploadCapabilityIsProtectedScopedAndOneTime(t *testing.T) {
	manager := &fakeManager{}
	handler := NewHandler(manager, Options{ControlPlaneToken: "moca-secret"})

	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/namespaces/team1/contexts/research/upload-capabilities", nil)
	unauthorized.Header.Set("X-Context-Subject", "user:alice")
	unauthorizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedResponse.Code)
	}

	capability := issueUpload(t, handler)
	if capability.Method != http.MethodPut || capability.ContentType != contextBundleType || capability.MaxBytes != maxUploadSize {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	if capability.UploadURL == "" || capability.Token == "" {
		t.Fatalf("incomplete capability: %+v", capability)
	}

	upload := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("compressed-context"))
	upload.Header.Set("Authorization", "Bearer "+capability.Token)
	upload.Header.Set("Content-Type", capability.ContentType)
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	if !bytes.Contains(uploadResponse.Body.Bytes(), []byte(`"workspacePath":".context-service/materialized/`)) {
		t.Fatalf("upload response missing workspacePath: %s", uploadResponse.Body.String())
	}
	if manager.uploadedNamespace != "team1" || manager.uploadedContext != "research" || string(manager.uploadedBundle) != "compressed-context" {
		t.Fatalf("upload target = %q/%q, body = %q", manager.uploadedNamespace, manager.uploadedContext, manager.uploadedBundle)
	}

	replay := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("replay"))
	replay.Header.Set("Authorization", "Bearer "+capability.Token)
	replay.Header.Set("Content-Type", capability.ContentType)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", replayResponse.Code)
	}
}

func TestContextUploadCapabilityRequiresExplicitAuthorizedSubject(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		manager *fakeManager
		status  int
	}{
		{name: "missing", manager: &fakeManager{}, status: http.StatusBadRequest},
		{name: "invalid", subject: "unknown:alice", manager: &fakeManager{}, status: http.StatusBadRequest},
		{name: "denied", subject: "user:alice", manager: &fakeManager{deniedPermission: contextresource.PermissionWrite}, status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/namespaces/team1/contexts/research/upload-capabilities", nil)
			request.Header.Set("Authorization", "Bearer moca-secret")
			if test.subject != "" {
				request.Header.Set("X-Context-Subject", test.subject)
			}
			response := httptest.NewRecorder()
			NewHandler(test.manager, Options{ControlPlaneToken: "moca-secret"}).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestContextUploadCapabilityExpires(t *testing.T) {
	h := &handler{manager: &fakeManager{}, uploads: newUploadCapabilities("moca-secret")}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	h.uploads.now = func() time.Time { return now }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/namespaces/{namespace}/contexts/{name}/upload-capabilities", h.issueContextUpload)
	mux.HandleFunc("PUT /v1/uploads/{id}", h.uploadContext)
	capability := issueUpload(t, mux)
	now = now.Add(uploadCapabilityTTL + time.Second)

	upload := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	upload.Header.Set("Authorization", "Bearer "+capability.Token)
	upload.Header.Set("Content-Type", capability.ContentType)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, upload)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired status = %d", response.Code)
	}
}

func TestWrongContentTypeDoesNotConsumeCapability(t *testing.T) {
	handler := NewHandler(&fakeManager{}, Options{ControlPlaneToken: "moca-secret"})
	capability := issueUpload(t, handler)
	wrong := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	wrong.Header.Set("Authorization", "Bearer "+capability.Token)
	wrong.Header.Set("Content-Type", "application/octet-stream")
	wrongResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong type status = %d", wrongResponse.Code)
	}

	retry := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	retry.Header.Set("Authorization", "Bearer "+capability.Token)
	retry.Header.Set("Content-Type", capability.ContentType)
	retryResponse := httptest.NewRecorder()
	handler.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, body = %s", retryResponse.Code, retryResponse.Body.String())
	}
}

func TestContextUploadMapsSizeLimit(t *testing.T) {
	manager := &fakeManager{uploadErr: &http.MaxBytesError{Limit: maxUploadSize}}
	handler := NewHandler(manager, Options{ControlPlaneToken: "moca-secret"})
	capability := issueUpload(t, handler)
	upload := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	upload.Header.Set("Authorization", "Bearer "+capability.Token)
	upload.Header.Set("Content-Type", capability.ContentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, upload)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	manager.uploadErr = nil
	retry := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	retry.Header.Set("Authorization", "Bearer "+capability.Token)
	retry.Header.Set("Content-Type", capability.ContentType)
	retryResponse := httptest.NewRecorder()
	handler.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, body = %s", retryResponse.Code, retryResponse.Body.String())
	}
}

func TestContextUploadRejectsOversizedContentLengthBeforeRedemption(t *testing.T) {
	manager := &fakeManager{}
	handler := NewHandler(manager, Options{ControlPlaneToken: "moca-secret"})
	capability := issueUpload(t, handler)
	upload := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	upload.ContentLength = maxUploadSize + 1
	upload.Header.Set("Authorization", "Bearer "+capability.Token)
	upload.Header.Set("Content-Type", capability.ContentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, upload)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	retry := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	retry.Header.Set("Authorization", "Bearer "+capability.Token)
	retry.Header.Set("Content-Type", capability.ContentType)
	retryResponse := httptest.NewRecorder()
	handler.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, body = %s", retryResponse.Code, retryResponse.Body.String())
	}
}

func TestCapabilityRejectsConcurrentRedemption(t *testing.T) {
	capabilities := newUploadCapabilities("secret")
	capabilities.put("id", uploadCapability{
		namespace: "team1", context: "research", storageUID: "uid",
		tokenHash: sha256.Sum256([]byte("token")), expiresAt: time.Now().Add(time.Minute), state: "issued",
	})
	if _, ok := capabilities.begin("id", "token"); !ok {
		t.Fatal("first redemption failed")
	}
	if _, ok := capabilities.begin("id", "token"); ok {
		t.Fatal("concurrent redemption succeeded")
	}
	if _, ok := capabilities.begin("id", "wrong-token"); ok {
		t.Fatal("wrong-token redemption succeeded")
	}
	capabilities.finish("id", false)
	if _, ok := capabilities.begin("id", "token"); !ok {
		t.Fatal("concurrent or wrong-token attempt destroyed the capability")
	}
}

func TestTrustedContextCreateAndAttachmentResolution(t *testing.T) {
	manager := &fakeManager{}
	handler := NewHandler(manager, Options{ControlPlaneToken: "moca-secret"})
	create := httptest.NewRequest(http.MethodPost, "/internal/v1/contexts", bytes.NewBufferString(`{
		"name":"research","namespace":"team1","type":"workspace",
		"storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce"}
	}`))
	create.Header.Set("Authorization", "Bearer moca-secret")
	// Moca identities already contain their provider prefix. The delegated
	// subject keeps that value as the name portion of the Context subject.
	create.Header.Set("X-Context-Subject", "user:github:alice")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}
	for _, expected := range []string{`"contextId":"pvc-uid"`, `"claimName":"context-research"`} {
		if !bytes.Contains(createResponse.Body.Bytes(), []byte(expected)) {
			t.Errorf("create response missing %s: %s", expected, createResponse.Body.String())
		}
	}
	if manager.createdContext.Owner != (contextresource.Subject{Kind: "user", Name: "github:alice"}) {
		t.Fatalf("owner = %+v", manager.createdContext.Owner)
	}

	resolve := httptest.NewRequest(http.MethodGet, "/internal/v1/namespaces/team1/contexts/research/attachment", nil)
	resolve.Header.Set("Authorization", "Bearer moca-secret")
	resolve.Header.Set("X-Context-Subject", "user:github:alice")
	resolveResponse := httptest.NewRecorder()
	handler.ServeHTTP(resolveResponse, resolve)
	if resolveResponse.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s", resolveResponse.Code, resolveResponse.Body.String())
	}
	for _, expected := range []string{`"contextId":"pvc-uid"`, `"status":"ready"`, `"currentRevision":"` + strings.Repeat("a", 64) + `"`, `"claimName":"context-research"`} {
		if !bytes.Contains(resolveResponse.Body.Bytes(), []byte(expected)) {
			t.Errorf("resolve response missing %s: %s", expected, resolveResponse.Body.String())
		}
	}
}

func TestFreezeContextReturnsExactAttachment(t *testing.T) {
	manager := &fakeManager{}
	handler := NewHandler(manager, Options{ControlPlaneToken: "moca-secret"})
	revision := strings.Repeat("a", 64)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/namespaces/team1/contexts/research/freeze", bytes.NewBufferString(`{"revision":"`+revision+`"}`))
	request.Header.Set("Authorization", "Bearer moca-secret")
	request.Header.Set("X-Context-Subject", "user:alice")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.frozenNamespace != "team1" || manager.frozenContext != "research" || manager.frozenRevisionArg != revision || manager.frozenStorageUID != "pvc-uid" {
		t.Fatalf("freeze target = %q/%q revision=%q uid=%q", manager.frozenNamespace, manager.frozenContext, manager.frozenRevisionArg, manager.frozenStorageUID)
	}
	for _, expected := range []string{`"contextId":"pvc-uid"`, `"currentRevision":"` + revision + `"`, `"claimName":"context-research"`} {
		if !bytes.Contains(response.Body.Bytes(), []byte(expected)) {
			t.Errorf("freeze response missing %s: %s", expected, response.Body.String())
		}
	}
}

func TestFreezeContextRejectsInvalidOrMismatchedRevision(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		manager   *fakeManager
		status    int
		errorCode string
	}{
		{name: "invalid digest", body: `{"revision":"latest"}`, manager: &fakeManager{}, status: http.StatusBadRequest, errorCode: "invalid_request"},
		{name: "mismatch", body: `{"revision":"` + strings.Repeat("b", 64) + `"}`, manager: &fakeManager{}, status: http.StatusConflict, errorCode: "revision_mismatch"},
		{name: "already frozen differently", body: `{"revision":"` + strings.Repeat("a", 64) + `"}`, manager: &fakeManager{freezeErr: contextresource.ErrFrozen}, status: http.StatusConflict, errorCode: "context_frozen"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/internal/v1/namespaces/team1/contexts/research/freeze", bytes.NewBufferString(test.body))
			request.Header.Set("Authorization", "Bearer moca-secret")
			request.Header.Set("X-Context-Subject", "user:alice")
			response := httptest.NewRecorder()
			NewHandler(test.manager, Options{ControlPlaneToken: "moca-secret"}).ServeHTTP(response, request)
			if response.Code != test.status || !bytes.Contains(response.Body.Bytes(), []byte(`"error":"`+test.errorCode+`"`)) {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFreezeContextRequiresReadAndAttachAccess(t *testing.T) {
	for _, permission := range []contextresource.Permission{contextresource.PermissionRead, contextresource.PermissionAttach} {
		t.Run(string(permission), func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/internal/v1/namespaces/team1/contexts/research/freeze", bytes.NewBufferString(`{"revision":"`+strings.Repeat("a", 64)+`"}`))
			request.Header.Set("Authorization", "Bearer moca-secret")
			request.Header.Set("X-Context-Subject", "user:alice")
			response := httptest.NewRecorder()
			NewHandler(&fakeManager{deniedPermission: permission}, Options{ControlPlaneToken: "moca-secret"}).ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFrozenContextRejectsCapabilityIssuance(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/namespaces/team1/contexts/research/upload-capabilities", nil)
	request.Header.Set("Authorization", "Bearer moca-secret")
	request.Header.Set("X-Context-Subject", "user:alice")
	response := httptest.NewRecorder()
	NewHandler(&fakeManager{frozenRevision: strings.Repeat("a", 64)}, Options{ControlPlaneToken: "moca-secret"}).ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte(`"error":"context_frozen"`)) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCapabilityIssuedBeforeFreezeFailsAtRedemption(t *testing.T) {
	manager := &fakeManager{}
	handler := NewHandler(manager, Options{ControlPlaneToken: "moca-secret"})
	capability := issueUpload(t, handler)
	manager.uploadErr = contextresource.ErrFrozen

	upload := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	upload.Header.Set("Authorization", "Bearer "+capability.Token)
	upload.Header.Set("Content-Type", capability.ContentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, upload)
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte(`"error":"context_frozen"`)) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPut, capability.UploadURL, bytes.NewBufferString("bundle"))
	replay.Header.Set("Authorization", "Bearer "+capability.Token)
	replay.Header.Set("Content-Type", capability.ContentType)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("frozen capability was not consumed: status = %d", replayResponse.Code)
	}
}

func TestTrustedContextRoutesRejectMissingTokenAndBootstrapSubject(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		subject string
		status  int
	}{
		{name: "missing token", subject: "user:alice", status: http.StatusUnauthorized},
		{name: "bootstrap subject", token: "moca-secret", subject: "service:context-service-admin", status: http.StatusBadRequest},
		{name: "anonymous user", token: "moca-secret", subject: "user:anonymous", status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/internal/v1/namespaces/team1/contexts/research/attachment", nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			request.Header.Set("X-Context-Subject", test.subject)
			response := httptest.NewRecorder()
			NewHandler(&fakeManager{}, Options{ControlPlaneToken: "moca-secret"}).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestTrustedContextDeleteRequiresControlPlaneAuthentication(t *testing.T) {
	manager := &fakeManager{}
	handler := NewHandler(manager, Options{ControlPlaneToken: "moca-secret"})
	request := httptest.NewRequest(http.MethodDelete, "/internal/v1/namespaces/team1/contexts/research", nil)
	request.Header.Set("Authorization", "Bearer moca-secret")
	request.Header.Set("X-Context-Subject", "user:alice")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !manager.deleteCalled {
		t.Fatalf("status = %d, deleteCalled = %v, body = %s", response.Code, manager.deleteCalled, response.Body.String())
	}
}
