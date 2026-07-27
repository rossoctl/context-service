package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rossoctl/context-service/internal/pool"
)

type fakeManager struct {
	created pool.CreateRequest
}

func (f *fakeManager) Create(_ context.Context, request pool.CreateRequest) (pool.Pool, error) {
	f.created = request
	return pool.Pool{Name: request.Name, Status: "provisioning", Replicas: request.Replicas}, nil
}
func (f *fakeManager) Get(_ context.Context, name string) (pool.Pool, error) {
	return pool.Pool{Name: name, Status: "ready"}, nil
}
func (f *fakeManager) Delete(_ context.Context, _ string) error { return nil }

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

func TestCreateRejectsSharedRWO(t *testing.T) {
	body := []byte(`{"name":"bad","replicas":2,"workspace":{"size":"1Gi","accessMode":"ReadWriteOnce"}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox-pools", bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(&fakeManager{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["error"] != "invalid_request" {
		t.Fatalf("unexpected response: %#v", result)
	}
}
