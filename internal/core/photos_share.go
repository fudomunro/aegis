package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// PhotoShareRequest is the agent-facing body for POST /v1/library/photos/share.
type PhotoShareRequest struct {
	// SourceService is the photo source connector ID (e.g. "immich").
	SourceService string `json:"source_service"`
	// SourceID is the service-local photo/asset ID.
	SourceID string `json:"source_id"`
	// TargetService is the share destination (default connectors.PhotoShareDefaultTarget).
	TargetService string `json:"target_service,omitempty"`
	// Caption is optional post text.
	Caption string `json:"caption,omitempty"`
	// Alt is optional image alt text.
	Alt string `json:"alt,omitempty"`
	// DryRun validates permissions and source fetch plan without publishing.
	DryRun bool `json:"dry_run,omitempty"`
}

// HandlePhotoShare publishes one photo to a share target and returns a PoC
// ownership claim (issue #20). Aegis still does not store photo bytes: the
// handler fetches the original from the canonical source for this request
// only, hashes it, publishes, and drops the buffer.
//
// POST /v1/library/photos/share
func (s *Server) HandlePhotoShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req PhotoShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			shared.WriteBodyReadError(w, err)
			return
		}
		shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.SourceService = strings.TrimSpace(req.SourceService)
	req.SourceID = strings.TrimSpace(req.SourceID)
	req.TargetService = strings.TrimSpace(req.TargetService)
	if req.TargetService == "" {
		req.TargetService = connectors.PhotoShareDefaultTarget
	}
	if req.SourceService == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service is required")
		return
	}
	if req.SourceID == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_id is required")
		return
	}
	if req.SourceService == req.TargetService {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service and target_service must differ")
		return
	}

	logger := withPeerLog(slog.With("handler", "photo_share",
		"source_service", req.SourceService,
		"source_id", req.SourceID,
		"target", req.TargetService,
		"dry_run", req.DryRun), r)

	if err := s.requireMirrorReadable(s.requestGrants(r), req.SourceService, "photos"); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := s.requireMirrorWritable(s.requestGrants(r), req.TargetService, "photos"); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	src, ok := s.connectors[req.SourceService]
	if !ok {
		shared.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("service %q is not connected", req.SourceService))
		return
	}
	bytesProvider, ok := src.(kinds.PhotoBytesProvider)
	if !ok {
		shared.WriteJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("service %q cannot fetch photo bytes for share yet (supported: %s)",
				req.SourceService, connectorIDsProviding[kinds.PhotoBytesProvider](s.connectors)))
		return
	}
	dst, ok := s.connectors[req.TargetService]
	if !ok {
		shared.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("service %q is not connected", req.TargetService))
		return
	}
	target, ok := dst.(kinds.PhotoShareTarget)
	if !ok {
		shared.WriteJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("service %q cannot accept photo shares yet (supported: %s)",
				req.TargetService, connectorIDsProviding[kinds.PhotoShareTarget](s.connectors)))
		return
	}

	data, mime, err := bytesProvider.FetchPhotoBytes(r.Context(), req.SourceID)
	if err != nil {
		s.writePhotoShareUpstreamError(w, logger, req.SourceService, err)
		return
	}
	if len(data) == 0 {
		shared.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("%s returned empty photo bytes", req.SourceService))
		return
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	logger = logger.With("bytes", len(data), "sha256", sha, "mime_type", mime)

	if req.DryRun {
		shared.WriteJSON(w, kinds.PhotoShareResponse{
			TargetService: req.TargetService,
			SourceService: req.SourceService,
			SourceID:      req.SourceID,
			DryRun:        true,
		})
		return
	}

	result, err := target.SharePhoto(r.Context(), kinds.PhotoShareRequest{
		SourceService: req.SourceService,
		SourceID:      req.SourceID,
		Data:          data,
		MimeType:      mime,
		Caption:       req.Caption,
		Alt:           req.Alt,
		ContentSHA256: sha,
	})
	if err != nil {
		s.writePhotoShareUpstreamError(w, logger, req.TargetService, err)
		return
	}

	sharedAt := result.SharedAt
	if sharedAt == "" {
		sharedAt = time.Now().UTC().Format(time.RFC3339)
	}
	claim := &kinds.PhotoShareClaim{
		Type:          "sha256+public-post",
		SHA256:        sha,
		MimeType:      mime,
		ByteSize:      len(data),
		SourceService: req.SourceService,
		SourceID:      req.SourceID,
		TargetService: req.TargetService,
		PostURI:       result.PostURI,
		PostURL:       result.PostURL,
		SharedAt:      sharedAt,
		Note: "PoC ownership claim: content hash of the shared bytes plus a " +
			"public post locator. Not a cryptographic proof (no signature or trusted timestamp).",
	}
	s.recordPhotoShareCopy(
		req.SourceService, req.SourceID, req.TargetService,
		result.PostID, result.PostURI, result.PostURL, sha, claim.Note)
	logger.Info("photo shared", "post_uri", result.PostURI, "post_url", result.PostURL)
	shared.WriteJSON(w, kinds.PhotoShareResponse{
		TargetService: req.TargetService,
		SourceService: req.SourceService,
		SourceID:      req.SourceID,
		PostURI:       result.PostURI,
		PostURL:       result.PostURL,
		PostID:        result.PostID,
		Claim:         claim,
	})
}

func (s *Server) writePhotoShareUpstreamError(w http.ResponseWriter, logger *slog.Logger, serviceID string, err error) {
	if errors.Is(err, kinds.ErrUnauthorized) || strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		logger.Warn("photo share unauthorized upstream", "service", serviceID, "error", err)
		shared.WriteJSONError(w, http.StatusUnauthorized, fmt.Sprintf("%s rejected the request — check its credentials", serviceID))
		return
	}
	logger.Error("photo share failed", "service", serviceID, "error", err)
	shared.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("photo share failed on %s: %v", serviceID, err))
}

// connectorIDsProviding names the registered connectors that implement the
// given provider interface, sorted. Used for "supported: …" copy so core
// never hardcodes a provider name (issue #254).
func connectorIDsProviding[T any](m map[string]connectors.ServiceConnector) string {
	var ids []string
	for id, c := range m {
		if _, ok := any(c).(T); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}
