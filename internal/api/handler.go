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
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}/revisions", h.listContextRevisions)
	mux.HandleFunc("POST /v1/namespaces/{namespace}/contexts/{name}/revisions", h.publishContextRevision)
	mux.HandleFunc("POST /v1/namespaces/{namespace}/contexts/{name}/snapshots", h.createContextSnapshot)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}/snapshots", h.listContextSnapshots)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}/snapshots/{snapshot}", h.getContextSnapshot)
	mux.HandleFunc("POST /v1/namespaces/{namespace}/contexts/{name}/clones", h.cloneContextSnapshot)
	mux.HandleFunc("POST /v1/namespaces/{namespace}/contexts/{name}/restore", h.restoreContextSnapshot)
	mux.HandleFunc("PUT /v1/namespaces/{namespace}/contexts/{name}/retention", h.setContextRetention)
	mux.HandleFunc("POST /v1/namespaces/{namespace}/contexts/{name}/gc", h.garbageCollectContextSnapshots)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}/capabilities", h.contextLifecycleCapabilities)
	mux.HandleFunc("PUT /v1/namespaces/{namespace}/contexts/{name}/query-index", h.publishContextQueryIndex)
	mux.HandleFunc("POST /v1/namespaces/{namespace}/query", h.queryContexts)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}/grants", h.listContextGrants)
	mux.HandleFunc("PUT /v1/namespaces/{namespace}/contexts/{name}/grants", h.setContextGrant)
	mux.HandleFunc("DELETE /v1/namespaces/{namespace}/contexts/{name}/grants", h.revokeContextGrant)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}/consumers", h.listContextConsumers)
	mux.HandleFunc("PUT /v1/namespaces/{namespace}/contexts/{name}/consumers", h.attachContextConsumer)
	mux.HandleFunc("DELETE /v1/namespaces/{namespace}/contexts/{name}/consumers", h.detachContextConsumer)
	mux.HandleFunc("GET /v1/namespaces/{namespace}/contexts/{name}/audit", h.listContextAudit)
	mux.HandleFunc("DELETE /v1/namespaces/{namespace}/contexts/{name}", h.deleteContext)
	return mux
}

func (h *handler) publishContextQueryIndex(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionWrite) {
		return
	}
	var index contextresource.QueryIndex
	if err := decodeBody(w, r, &index); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.manager.PublishContextQueryIndex(r.Context(), r.PathValue("namespace"), r.PathValue("name"), index); err != nil {
		writeContextError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) queryContexts(w http.ResponseWriter, r *http.Request) {
	var request contextresource.QueryRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.manager.QueryContexts(r.Context(), r.PathValue("namespace"), requestSubject(r), request)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) createContextSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionWrite) {
		return
	}
	var request contextresource.SnapshotRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateResourceName("snapshot", request.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.manager.CreateContextSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("name"), request)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *handler) listContextSnapshots(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionRead) {
		return
	}
	items, err := h.manager.ListContextSnapshots(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contextresource.SnapshotList{Items: items})
}

func (h *handler) getContextSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionRead) {
		return
	}
	result, err := h.manager.GetContextSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("name"), r.PathValue("snapshot"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) cloneContextSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionDerive) {
		return
	}
	var request contextresource.CloneRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateResourceName("name", request.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateResourceName("snapshot", request.Snapshot); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Owner = requestSubject(r)
	result, err := h.manager.CloneContextSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("name"), request)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *handler) restoreContextSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionWrite) {
		return
	}
	var request contextresource.RestoreRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateResourceName("snapshot", request.Snapshot); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.manager.RestoreContextSnapshot(r.Context(), r.PathValue("namespace"), r.PathValue("name"), request)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) setContextRetention(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionAdminister) {
		return
	}
	var request contextresource.RetentionRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.manager.SetContextRetention(r.Context(), r.PathValue("namespace"), r.PathValue("name"), request)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) garbageCollectContextSnapshots(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionAdminister) {
		return
	}
	result, err := h.manager.GarbageCollectContextSnapshots(r.Context(), r.PathValue("namespace"), r.PathValue("name"), r.URL.Query().Get("dryRun") == "true")
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) contextLifecycleCapabilities(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionRead) {
		return
	}
	result, err := h.manager.ContextLifecycleCapabilities(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func validateResourceName(label, name string) error {
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return fmt.Errorf("%s must be a Kubernetes name", label)
	}
	return nil
}

func (h *handler) listContextRevisions(w http.ResponseWriter, r *http.Request) {
	result, err := h.manager.AccessContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"), requestSubject(r), contextresource.PermissionRead)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contextresource.RevisionList{Items: result.Revisions})
}

func (h *handler) publishContextRevision(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var revision contextresource.Revision
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&revision); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateRevision(revision); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := h.manager.AccessContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"), requestSubject(r), contextresource.PermissionWrite); err != nil {
		writeContextError(w, err)
		return
	}
	result, err := h.manager.PublishContextRevision(r.Context(), r.PathValue("namespace"), r.PathValue("name"), revision)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func validateRevision(revision contextresource.Revision) error {
	if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, revision.ID); !matched {
		return errors.New("revision id must be a lowercase SHA-256 digest")
	}
	if revision.CreatedAt.IsZero() {
		return errors.New("revision creation time is required")
	}
	if strings.TrimSpace(revision.Operation) == "" {
		return errors.New("revision operation is required")
	}
	for _, source := range revision.Sources {
		if source.Context == "" || source.Type == "" {
			return errors.New("revision sources require context and type")
		}
		if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, source.Revision); !matched {
			return errors.New("source revision must be a lowercase SHA-256 digest")
		}
	}
	return nil
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
	items, err := h.manager.ListAccessibleContexts(r.Context(), r.PathValue("namespace"), requestSubject(r))
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
	request.Owner = requestSubject(r)
	created, err := h.manager.CreateContext(r.Context(), request)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handler) getContext(w http.ResponseWriter, r *http.Request) {
	result, err := h.manager.AccessContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"), requestSubject(r), contextresource.PermissionRead)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) deleteContext(w http.ResponseWriter, r *http.Request) {
	if _, err := h.manager.AccessContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"), requestSubject(r), contextresource.PermissionAdminister); err != nil {
		writeContextError(w, err)
		return
	}
	var err error
	if r.URL.Query().Get("force") == "true" {
		err = h.manager.ForceDeleteContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	} else {
		err = h.manager.DeleteContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	}
	if err != nil {
		writeContextError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func requestSubject(r *http.Request) contextresource.Subject {
	value := strings.TrimSpace(r.Header.Get("X-Context-Subject"))
	kind, name, found := strings.Cut(value, ":")
	if !found || kind == "" || name == "" {
		return contextresource.Subject{Kind: "user", Name: "anonymous"}
	}
	return contextresource.Subject{Kind: kind, Name: name}
}

func (h *handler) listContextGrants(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionAdminister) {
		return
	}
	items, err := h.manager.ListContextGrants(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contextresource.GrantList{Items: items})
}

func (h *handler) setContextGrant(w http.ResponseWriter, r *http.Request) {
	actor := requestSubject(r)
	if !h.requireContextAccess(w, r, contextresource.PermissionAdminister) {
		return
	}
	var grant contextresource.Grant
	if err := decodeBody(w, r, &grant); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateGrant(grant); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.manager.SetContextGrant(r.Context(), r.PathValue("namespace"), r.PathValue("name"), grant, actor)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) revokeContextGrant(w http.ResponseWriter, r *http.Request) {
	actor := requestSubject(r)
	if !h.requireContextAccess(w, r, contextresource.PermissionAdminister) {
		return
	}
	var subject contextresource.Subject
	if err := decodeBody(w, r, &subject); err != nil || !validSubject(subject) {
		writeError(w, http.StatusBadRequest, "invalid_request", "valid subject is required")
		return
	}
	result, err := h.manager.RevokeContextGrant(r.Context(), r.PathValue("namespace"), r.PathValue("name"), subject, actor)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) listContextConsumers(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionRead) {
		return
	}
	items, err := h.manager.ListContextConsumers(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contextresource.ConsumerList{Items: items})
}

func (h *handler) attachContextConsumer(w http.ResponseWriter, r *http.Request) {
	h.setConsumer(w, r, true)
}
func (h *handler) detachContextConsumer(w http.ResponseWriter, r *http.Request) {
	h.setConsumer(w, r, false)
}
func (h *handler) setConsumer(w http.ResponseWriter, r *http.Request, attached bool) {
	actor := requestSubject(r)
	if !h.requireContextAccess(w, r, contextresource.PermissionAttach) {
		return
	}
	var consumer contextresource.Consumer
	if err := decodeBody(w, r, &consumer); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if consumer.Subject == (contextresource.Subject{}) {
		consumer.Subject = actor
	}
	if !sameSubject(consumer.Subject, actor) {
		if _, err := h.manager.AccessContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"), actor, contextresource.PermissionAdminister); err != nil {
			writeContextError(w, err)
			return
		}
	}
	if !validSubject(consumer.Subject) || consumer.Kind == "" || consumer.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "consumer subject, kind, and name are required")
		return
	}
	if attached && consumer.AccessMode != "readOnly" && consumer.AccessMode != "readWrite" {
		writeError(w, http.StatusBadRequest, "invalid_request", "consumer accessMode must be readOnly or readWrite")
		return
	}
	if attached && consumer.AccessMode == "readWrite" {
		if _, err := h.manager.AccessContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"), actor, contextresource.PermissionWrite); err != nil {
			writeContextError(w, err)
			return
		}
	}
	result, err := h.manager.SetContextConsumer(r.Context(), r.PathValue("namespace"), r.PathValue("name"), consumer, attached, actor)
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) listContextAudit(w http.ResponseWriter, r *http.Request) {
	if !h.requireContextAccess(w, r, contextresource.PermissionAdminister) {
		return
	}
	items, err := h.manager.ListContextAudit(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contextresource.AuditList{Items: items})
}

func (h *handler) requireContextAccess(w http.ResponseWriter, r *http.Request, permission contextresource.Permission) bool {
	_, err := h.manager.AccessContext(r.Context(), r.PathValue("namespace"), r.PathValue("name"), requestSubject(r), permission)
	if err != nil {
		writeContextError(w, err)
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, output any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

func validSubject(subject contextresource.Subject) bool {
	switch subject.Kind {
	case "user", "agent", "workload", "service":
	default:
		return false
	}
	return strings.TrimSpace(subject.Name) != ""
}

func validateGrant(grant contextresource.Grant) error {
	if !validSubject(grant.Subject) || len(grant.Permissions) == 0 {
		return errors.New("grant requires a valid subject and permissions")
	}
	seen := map[contextresource.Permission]bool{}
	for _, permission := range grant.Permissions {
		switch permission {
		case contextresource.PermissionRead, contextresource.PermissionWrite, contextresource.PermissionAttach, contextresource.PermissionDerive, contextresource.PermissionAdminister:
		default:
			return fmt.Errorf("unsupported permission %q", permission)
		}
		if seen[permission] {
			return fmt.Errorf("duplicate permission %q", permission)
		}
		seen[permission] = true
	}
	for _, permission := range []contextresource.Permission{contextresource.PermissionWrite, contextresource.PermissionAttach, contextresource.PermissionDerive} {
		if seen[permission] && !seen[contextresource.PermissionRead] {
			return fmt.Errorf("permission %q requires read", permission)
		}
	}
	return nil
}

func sameSubject(left, right contextresource.Subject) bool {
	return left.Kind == right.Kind && left.Name == right.Name
}

func validateContext(request contextresource.CreateRequest) error {
	if len(request.Name) == 0 || len(request.Name) > 50 || !poolNamePattern.MatchString(request.Name) {
		return errors.New("name must be a lowercase Kubernetes name no longer than 50 characters")
	}
	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) > 0 {
		return errors.New("namespace must be a lowercase Kubernetes name")
	}
	switch request.Type {
	case "workspace", "state", "memory", "knowledge", "artifacts":
	default:
		return errors.New("type must be workspace, state, memory, knowledge, or artifacts")
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
	case errors.Is(err, contextresource.ErrInUse):
		writeError(w, http.StatusConflict, "in_use", err.Error())
	case errors.Is(err, contextresource.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, contextresource.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, "unsupported", err.Error())
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
