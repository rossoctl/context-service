package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
)

const (
	uploadCapabilityTTL = 5 * time.Minute
	maxUploadSize       = 256 << 20
	contextBundleType   = "application/vnd.rossoctl.context"
	frozenBundleType    = contextBundleType
)

type uploadCapability struct {
	namespace  string
	context    string
	storageUID string
	tokenHash  [sha256.Size]byte
	expiresAt  time.Time
	state      string
}

func (h *handler) exportFrozenContext(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authorizeControlPlaneSubject(w, r)
	if !ok {
		return
	}
	namespace, name, revision := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("revision")
	if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, revision); !matched {
		writeError(w, http.StatusBadRequest, "invalid_request", "revision must be a lowercase SHA-256 digest")
		return
	}
	resource, err := h.manager.AccessContext(r.Context(), namespace, name, subject, contextresource.PermissionRead)
	if err != nil {
		writeContextError(w, err)
		return
	}
	if resource.StorageUID == "" || resource.CurrentRevision != revision || resource.FrozenRevision != revision {
		writeContextError(w, contextresource.ErrNotFound)
		return
	}
	output := &exportResponseWriter{w: w, revision: revision}
	if err := h.manager.ExportFrozenContext(r.Context(), namespace, name, revision, resource.StorageUID, output); err != nil {
		if !output.started {
			writeContextError(w, err)
			return
		}
		// The 200 and part of the archive are already on the wire. Abort the
		// connection so the client sees a transport error, not a clean response.
		panic(http.ErrAbortHandler)
	}
	output.commit()
}

// exportResponseWriter defers the 200 until the first archive byte, so failures
// before streaming starts still produce an error status.
type exportResponseWriter struct {
	w        http.ResponseWriter
	revision string
	started  bool
}

func (e *exportResponseWriter) commit() {
	if e.started {
		return
	}
	e.started = true
	e.w.Header().Set("Cache-Control", "no-store")
	e.w.Header().Set("Content-Type", frozenBundleType)
	e.w.Header().Set("Content-Disposition", `attachment; filename="context-`+e.revision+`.context"`)
	e.w.WriteHeader(http.StatusOK)
}

func (e *exportResponseWriter) Write(data []byte) (int, error) {
	e.commit()
	return e.w.Write(data)
}

type uploadCapabilities struct {
	mu                sync.Mutex
	items             map[string]uploadCapability
	controlPlaneToken string
	now               func() time.Time
}

type uploadCapabilityResponse struct {
	UploadURL   string    `json:"uploadUrl"`
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Method      string    `json:"method"`
	ContentType string    `json:"contentType"`
	MaxBytes    int64     `json:"maxBytes"`
}

// trustedContext is the Context view returned to Moca. It omits the PVC
// attachment because Moca receives content as bundles, not mounts.
type trustedContext struct {
	ContextID       string `json:"contextId"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Type            string `json:"type"`
	Status          string `json:"status"`
	CurrentRevision string `json:"currentRevision,omitempty"`
	FrozenRevision  string `json:"frozenRevision,omitempty"`
}

type freezeContextRequest struct {
	Revision string `json:"revision"`
}

func newUploadCapabilities(controlPlaneToken string) *uploadCapabilities {
	return &uploadCapabilities{items: map[string]uploadCapability{}, controlPlaneToken: controlPlaneToken, now: time.Now}
}

func (h *handler) createTrustedContext(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authorizeControlPlaneSubject(w, r)
	if !ok {
		return
	}
	h.createContextAs(w, r, subject, true)
}

func (h *handler) deleteTrustedContext(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authorizeControlPlaneSubject(w, r)
	if !ok {
		return
	}
	h.deleteContextAs(w, r, subject)
}

func (h *handler) freezeContext(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authorizeControlPlaneSubject(w, r)
	if !ok {
		return
	}
	var request freezeContextRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, request.Revision); !matched {
		writeError(w, http.StatusBadRequest, "invalid_request", "revision must be a lowercase SHA-256 digest")
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	resource, err := h.manager.AccessContext(r.Context(), namespace, name, subject, contextresource.PermissionAttach)
	if err != nil {
		writeContextError(w, err)
		return
	}
	readable, err := h.manager.AccessContext(r.Context(), namespace, name, subject, contextresource.PermissionRead)
	if err != nil {
		writeContextError(w, err)
		return
	}
	if resource.StorageUID == "" || readable.StorageUID != resource.StorageUID {
		writeError(w, http.StatusConflict, "storage_identity_changed", "context storage identity changed during authorization")
		return
	}
	result, err := h.manager.FreezeContext(r.Context(), namespace, name, request.Revision, resource.StorageUID)
	if err != nil {
		writeContextError(w, err)
		return
	}
	if result.StorageUID == "" {
		writeError(w, http.StatusInternalServerError, "storage_identity_unavailable", "context storage identity is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, trustedContextResponse(result))
}

func trustedContextResponse(resource contextresource.Resource) trustedContext {
	return trustedContext{
		ContextID: resource.StorageUID, Name: resource.Name, Namespace: resource.Namespace, Type: resource.Type,
		Status: resource.Status, CurrentRevision: resource.CurrentRevision, FrozenRevision: resource.FrozenRevision,
	}
}

func (h *handler) authorizeControlPlaneSubject(w http.ResponseWriter, r *http.Request) (contextresource.Subject, bool) {
	if !h.uploads.authorizeControlPlane(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid control-plane bearer token required")
		return contextresource.Subject{}, false
	}
	subject, ok := explicitRequestSubject(r)
	if !ok || !validSubject(subject) ||
		(subject.Kind == "service" && subject.Name == "context-service-admin") ||
		(subject.Kind == "user" && subject.Name == "anonymous") {
		writeError(w, http.StatusBadRequest, "invalid_subject", "X-Context-Subject must contain a valid delegated kind:name")
		return contextresource.Subject{}, false
	}
	return subject, true
}

func (h *handler) issueContextUpload(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authorizeControlPlaneSubject(w, r)
	if !ok {
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	resource, err := h.manager.AccessContext(r.Context(), namespace, name, subject, contextresource.PermissionWrite)
	if err != nil {
		writeContextError(w, err)
		return
	}
	if resource.StorageUID == "" {
		writeError(w, http.StatusInternalServerError, "storage_identity_unavailable", "context storage identity is unavailable")
		return
	}
	if resource.FrozenRevision != "" {
		writeContextError(w, contextresource.ErrFrozen)
		return
	}
	id, err := randomToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "create upload capability")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "create upload capability")
		return
	}
	expiresAt := h.uploads.now().Add(uploadCapabilityTTL).UTC()
	h.uploads.put(id, uploadCapability{
		namespace: namespace, context: name,
		storageUID: resource.StorageUID, tokenHash: sha256.Sum256([]byte(token)), expiresAt: expiresAt, state: "issued",
	})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, uploadCapabilityResponse{
		UploadURL: "/v1/uploads/" + id, Token: token, ExpiresAt: expiresAt,
		Method: http.MethodPut, ContentType: contextBundleType, MaxBytes: maxUploadSize,
	})
}

func (h *handler) uploadContext(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if mediaType != contextBundleType {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be "+contextBundleType)
		return
	}
	if r.ContentLength > maxUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "compressed context bundle exceeds 256 MiB")
		return
	}
	token, ok := bearerToken(r)
	capability, valid := h.uploads.begin(r.PathValue("id"), token)
	if !ok || !valid {
		writeError(w, http.StatusUnauthorized, "unauthorized", "upload capability is invalid, expired, or already used")
		return
	}
	defer r.Body.Close()
	body := http.MaxBytesReader(w, r.Body, maxUploadSize)
	result, err := h.manager.UploadContext(r.Context(), capability.namespace, capability.context, capability.storageUID, body)
	if err != nil {
		h.uploads.finish(r.PathValue("id"), errors.Is(err, contextresource.ErrFrozen))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || strings.Contains(err.Error(), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "compressed context bundle exceeds 256 MiB")
			return
		}
		switch {
		case errors.Is(err, contextresource.ErrInvalid),
			errors.Is(err, contextresource.ErrNotFound),
			errors.Is(err, contextresource.ErrFrozen):
			writeContextError(w, err)
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "context upload failed")
		}
		return
	}
	h.uploads.finish(r.PathValue("id"), true)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, result)
}

func (s *uploadCapabilities) authorizeControlPlane(r *http.Request) bool {
	token, ok := bearerToken(r)
	if !ok || s.controlPlaneToken == "" {
		return false
	}
	provided := sha256.Sum256([]byte(token))
	expected := sha256.Sum256([]byte(s.controlPlaneToken))
	return subtle.ConstantTimeCompare(provided[:], expected[:]) == 1
}

func (s *uploadCapabilities) put(id string, capability uploadCapability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, item := range s.items {
		if !item.expiresAt.After(now) {
			delete(s.items, key)
		}
	}
	s.items[id] = capability
}

func (s *uploadCapabilities) begin(id, token string) (uploadCapability, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capability, ok := s.items[id]
	if !ok {
		return uploadCapability{}, false
	}
	actual := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(actual[:], capability.tokenHash[:]) != 1 {
		return uploadCapability{}, false
	}
	if !capability.expiresAt.After(s.now()) {
		delete(s.items, id)
		return uploadCapability{}, false
	}
	if capability.state != "issued" {
		return uploadCapability{}, false
	}
	capability.state = "in_flight"
	s.items[id] = capability
	return capability, true
}

func (s *uploadCapabilities) finish(id string, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capability, ok := s.items[id]
	if !ok || capability.state != "in_flight" {
		return
	}
	if success || !capability.expiresAt.After(s.now()) {
		delete(s.items, id)
		return
	}
	capability.state = "issued"
	s.items[id] = capability
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	token = strings.TrimSpace(token)
	return token, found && strings.EqualFold(scheme, "Bearer") && token != ""
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
