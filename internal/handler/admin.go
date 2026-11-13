package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"kenosm/internal/model"
	"kenosm/internal/repository"
)

var (
	slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Admin exposes CRUD endpoints for configuring services (upstream + proxy).
type Admin struct {
	repo *repository.ServiceRepo
}

func NewAdmin(repo *repository.ServiceRepo) *Admin {
	return &Admin{repo: repo}
}

// serviceInput uses pointer fields so partial updates can distinguish
// "not provided" from "set to empty".
type serviceInput struct {
	Slug           *string `json:"slug"`
	Name           *string `json:"name"`
	UpstreamURL    *string `json:"upstream_url"`
	UpstreamAPIKey *string `json:"upstream_api_key"`
	AuthHeader     *string `json:"auth_header"`
	AuthScheme     *string `json:"auth_scheme"`
	ProxyHost      *string `json:"proxy_host"`
	ProxyPort      *int    `json:"proxy_port"`
	ProxyUsername  *string `json:"proxy_username"`
	ProxyPassword  *string `json:"proxy_password"`
	Model          *string `json:"model"`
	Enabled        *bool   `json:"enabled"`
}

func (a *Admin) Create(w http.ResponseWriter, r *http.Request) {
	var in serviceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	svc := model.Service{
		Slug:           normalizeSlug(deref(in.Slug)),
		Name:           deref(in.Name),
		UpstreamURL:    deref(in.UpstreamURL),
		UpstreamAPIKey: deref(in.UpstreamAPIKey),
		AuthHeader:     defaultString(in.AuthHeader, "Authorization"),
		AuthScheme:     defaultString(in.AuthScheme, "Bearer"),
		ProxyHost:      deref(in.ProxyHost),
		ProxyPort:      derefInt(in.ProxyPort),
		ProxyUsername:  deref(in.ProxyUsername),
		ProxyPassword:  deref(in.ProxyPassword),
		Model:          deref(in.Model),
		Enabled:        defaultBool(in.Enabled, true),
	}

	if err := validateService(&svc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	created, err := a.repo.Create(r.Context(), svc)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *Admin) List(w http.ResponseWriter, r *http.Request) {
	items, err := a.repo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *Admin) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	svc, err := a.repo.GetByID(r.Context(), id)
	if err != nil {
		writeRepoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, svc)
}

func (a *Admin) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var in serviceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	cur, err := a.repo.GetByID(r.Context(), id)
	if err != nil {
		writeRepoError(w, err)
		return
	}

	svc := cur
	if in.Slug != nil {
		svc.Slug = normalizeSlug(*in.Slug)
	}
	if in.Name != nil {
		svc.Name = *in.Name
	}
	if in.UpstreamURL != nil {
		svc.UpstreamURL = *in.UpstreamURL
	}
	if in.UpstreamAPIKey != nil {
		svc.UpstreamAPIKey = *in.UpstreamAPIKey
	}
	if in.AuthHeader != nil {
		svc.AuthHeader = *in.AuthHeader
	}
	if in.AuthScheme != nil {
		svc.AuthScheme = *in.AuthScheme
	}
	if in.ProxyHost != nil {
		svc.ProxyHost = *in.ProxyHost
	}
	if in.ProxyPort != nil {
		svc.ProxyPort = *in.ProxyPort
	}
	if in.ProxyUsername != nil {
		svc.ProxyUsername = *in.ProxyUsername
	}
	if in.ProxyPassword != nil {
		svc.ProxyPassword = *in.ProxyPassword
	}
	if in.Model != nil {
		svc.Model = *in.Model
	}
	if in.Enabled != nil {
		svc.Enabled = *in.Enabled
	}

	if err := validateService(&svc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	updated, err := a.repo.Update(r.Context(), svc)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *Admin) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateService(s *model.Service) error {
	if s.Slug == "" {
		return errors.New("slug is required")
	}
	if !slugRe.MatchString(s.Slug) {
		return errors.New("slug must be lowercase alphanumeric with optional dashes")
	}
	u, err := url.Parse(s.UpstreamURL)
	if err != nil {
		return errors.New("upstream_url is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("upstream_url must start with http:// or https://")
	}
	if u.Host == "" {
		return errors.New("upstream_url must include a host")
	}
	if s.ProxyHost == "" {
		return errors.New("proxy_host is required")
	}
	if s.ProxyPort < 1 || s.ProxyPort > 65535 {
		return errors.New("proxy_port must be between 1 and 65535")
	}
	return nil
}

func writeRepoError(w http.ResponseWriter, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func defaultString(s *string, def string) string {
	if s == nil || *s == "" {
		return def
	}
	return *s
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

func defaultBool(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func normalizeSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
