package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rossoctl/context-service/internal/pool"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const ReadHeaderTimeout = 5 * time.Second

var poolNamePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

type handler struct {
	manager pool.Manager
}

func NewHandler(manager pool.Manager) http.Handler {
	h := &handler{manager: manager}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /v1/sandbox-pools", h.create)
	mux.HandleFunc("GET /v1/sandbox-pools/{name}", h.get)
	mux.HandleFunc("DELETE /v1/sandbox-pools/{name}", h.delete)
	return mux
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
