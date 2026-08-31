package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
	"github.com/rossoctl/context-service/internal/storageclass"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const ReadHeaderTimeout = 5 * time.Second

var poolNamePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

type handler struct {
	manager interface {
		pool.Manager
		contextresource.Manager
		storageclass.Manager
	}
}

func NewHandler(manager interface {
	pool.Manager
	contextresource.Manager
	storageclass.Manager
}) http.Handler {
	h := &handler{manager: manager}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /v1/sandbox-pools", h.create)
	mux.HandleFunc("GET /v1/sandbox-pools", h.list)
	mux.HandleFunc("GET /v1/sandbox-pools/{name}", h.get)
	mux.HandleFunc("DELETE /v1/sandbox-pools/{name}", h.delete)
	mux.HandleFunc("POST /v1/contexts", h.createContext)
	mux.HandleFunc("GET /v1/storage-classes", h.listStorageClasses)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts", h.listContexts)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}", h.getContext)
	mux.HandleFunc("DELETE /v1/namespaces/{namespace}/contexts/{name}", h.deleteContext)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.manager.List(r.Context())
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pool.List{Items: items})
}

func (h *handler) listStorageClasses(w http.ResponseWriter, r *http.Request) {
	items, err := h.manager.ListStorageClasses(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, storageclass.List{Items: items})
}

func (h *handler) listContexts(w http.ResponseWriter, r *http.Request) {
	items, err := h.manager.ListContexts(r.Context(), r.PathValue("namespace"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contextresource.List{Items: items})
}

func (h *handler) createContext(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var request contextresource.CreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateContext(request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	created, err := h.manager.CreateContext(r.Context(), request)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handler) getContext(w http.ResponseWriter, r *http.Request) {
	result, err := h.manager.GetContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) deleteContext(w http.ResponseWriter, r *http.Request) {
	if err := h.manager.DeleteContext(r.Context(), r.PathValue("namespace"), r.PathValue("name")); err != nil {
		writeContextError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateContext(request contextresource.CreateRequest) error {
	if len(request.Name) == 0 || len(request.Name) > 50 || !poolNamePattern.MatchString(request.Name) {
		return errors.New("name must be a lowercase Kubernetes name no longer than 50 characters")
	}
	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) > 0 {
		return errors.New("namespace must be a lowercase Kubernetes name")
	}
	switch request.Type {
	case "workspace", "memory", "knowledge", "artifacts":
	default:
		return errors.New("type must be workspace, memory, knowledge, or artifacts")
	}
	if request.Storage.Backend != "pvc" {
		return errors.New("storage.backend must be pvc")
	}
	quantity, err := resource.ParseQuantity(request.Storage.Size)
	if err != nil || quantity.Sign() <= 0 {
		return errors.New("storage.size must be a positive Kubernetes quantity")
	}
	if request.Storage.AccessMode != "ReadWriteOnce" && request.Storage.AccessMode != "ReadWriteMany" {
		return errors.New("storage.accessMode must be ReadWriteOnce or ReadWriteMany")
	}
	return nil
}

func writeContextError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, contextresource.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "already_exists", err.Error())
	case errors.Is(err, contextresource.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, contextresource.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var request pool.CreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateCreate(request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	created, err := h.manager.Create(r.Context(), request)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	result, err := h.manager.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.manager.Delete(r.Context(), r.PathValue("name")); err != nil {
		writeManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateCreate(request pool.CreateRequest) error {
	if len(request.Name) == 0 || len(request.Name) > 50 || !poolNamePattern.MatchString(request.Name) {
		return errors.New("name must be a lowercase Kubernetes name no longer than 50 characters")
	}
	if request.Replicas < 1 || request.Replicas > 100 {
		return errors.New("replicas must be between 1 and 100")
	}
	if request.SandboxProfile != "" {
		if problems := validation.IsDNS1123Subdomain(request.SandboxProfile); len(problems) > 0 {
			return errors.New("sandboxProfile must be a lowercase Kubernetes name")
		}
		if request.WarmPoolRef != "" {
			return errors.New("sandboxProfile cannot be combined with warmPoolRef; the warm pool already selects a template")
		}
	}
	if request.WarmPoolRef != "" {
		if problems := validation.IsDNS1123Subdomain(request.WarmPoolRef); len(problems) > 0 {
			return errors.New("warmPoolRef must be a lowercase Kubernetes name")
		}
		if request.Workspace != (pool.Workspace{}) {
			return errors.New("warmPoolRef cannot be combined with workspace settings")
		}
		return nil
	}
	if request.Workspace.ClaimName != "" {
		if problems := validation.IsDNS1123Subdomain(request.Workspace.ClaimName); len(problems) > 0 {
			return errors.New("workspace.claimName must be a lowercase Kubernetes name")
		}
		if request.Workspace.Size != "" || request.Workspace.AccessMode != "" || request.Workspace.StorageClass != "" {
			return errors.New("workspace.claimName cannot be combined with size, accessMode, or storageClass")
		}
		if request.Workspace.ReadOnly == nil {
			return errors.New("workspace.readOnly is required with workspace.claimName")
		}
		return nil
	}
	if request.Workspace.ReadOnly != nil {
		return errors.New("workspace.readOnly requires workspace.claimName")
	}
	if strings.TrimSpace(request.Workspace.Size) == "" {
		return errors.New("workspace.size is required")
	}
	quantity, err := resource.ParseQuantity(request.Workspace.Size)
	if err != nil || quantity.Sign() <= 0 {
		return errors.New("workspace.size must be a positive Kubernetes quantity")
	}
	switch request.Workspace.AccessMode {
	case "ReadWriteMany":
	case "ReadWriteOnce":
	default:
		return fmt.Errorf("workspace.accessMode must be ReadWriteMany or ReadWriteOnce")
	}
	return nil
}

func writeManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pool.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "already_exists", err.Error())
	case errors.Is(err, pool.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, pool.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
