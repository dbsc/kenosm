package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"kenosm/internal/crypto"
	"kenosm/internal/model"
)

// ErrNotFound is returned when a service does not exist.
var ErrNotFound = errors.New("service not found")

const serviceColumns = "id, slug, name, upstream_url, upstream_api_key, auth_header, auth_scheme, proxy_host, proxy_port, proxy_username, proxy_password, model, enabled, created_at, updated_at"

// ServiceRepo provides persistence for Service records, encrypting and
// decrypting secrets transparently.
type ServiceRepo struct {
	pool   *pgxpool.Pool
	cipher *crypto.Cipher
}

func NewServiceRepo(pool *pgxpool.Pool, c *crypto.Cipher) *ServiceRepo {
	return &ServiceRepo{pool: pool, cipher: c}
}

func (r *ServiceRepo) Create(ctx context.Context, svc model.Service) (model.Service, error) {
	encKey, err := r.cipher.Encrypt(svc.UpstreamAPIKey)
	if err != nil {
		return model.Service{}, err
	}
	encPass, err := r.cipher.Encrypt(svc.ProxyPassword)
	if err != nil {
		return model.Service{}, err
	}

	row := r.pool.QueryRow(ctx,
		`INSERT INTO services
			(slug, name, upstream_url, upstream_api_key, auth_header, auth_scheme,
			 proxy_host, proxy_port, proxy_username, proxy_password, model, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 RETURNING `+serviceColumns,
		svc.Slug, svc.Name, svc.UpstreamURL, encKey, svc.AuthHeader, svc.AuthScheme,
		svc.ProxyHost, svc.ProxyPort, svc.ProxyUsername, encPass, svc.Model, svc.Enabled,
	)
	return scanService(row, r.cipher, true)
}

func (r *ServiceRepo) GetByID(ctx context.Context, id string) (model.Service, error) {
	uid, err := uuidArg(id)
	if err != nil {
		return model.Service{}, err
	}
	row := r.pool.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE id = $1`, uid)
	return scanService(row, r.cipher, true)
}

func (r *ServiceRepo) GetBySlug(ctx context.Context, slug string) (model.Service, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE slug = $1`, slug)
	return scanService(row, r.cipher, true)
}

func (r *ServiceRepo) List(ctx context.Context) ([]model.Service, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+serviceColumns+` FROM services ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Service, 0)
	for rows.Next() {
		svc, err := scanService(rows, r.cipher, false)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, rows.Err()
}

// ListWithSecrets returns enabled services with decrypted secrets. Used
// internally (e.g. by the swarm) and never exposed over the API.
func (r *ServiceRepo) ListWithSecrets(ctx context.Context) ([]model.Service, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+serviceColumns+` FROM services WHERE enabled ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Service, 0)
	for rows.Next() {
		svc, err := scanService(rows, r.cipher, true)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, rows.Err()
}

func (r *ServiceRepo) Update(ctx context.Context, svc model.Service) (model.Service, error) {
	encKey, err := r.cipher.Encrypt(svc.UpstreamAPIKey)
	if err != nil {
		return model.Service{}, err
	}
	encPass, err := r.cipher.Encrypt(svc.ProxyPassword)
	if err != nil {
		return model.Service{}, err
	}

	uid, err := uuidArg(svc.ID)
	if err != nil {
		return model.Service{}, err
	}

	row := r.pool.QueryRow(ctx,
		`UPDATE services SET
			slug = $2, name = $3, upstream_url = $4, upstream_api_key = $5,
			auth_header = $6, auth_scheme = $7, proxy_host = $8, proxy_port = $9,
			proxy_username = $10, proxy_password = $11, model = $12, enabled = $13, updated_at = now()
		 WHERE id = $1
		 RETURNING `+serviceColumns,
		uid, svc.Slug, svc.Name, svc.UpstreamURL, encKey, svc.AuthHeader, svc.AuthScheme,
		svc.ProxyHost, svc.ProxyPort, svc.ProxyUsername, encPass, svc.Model, svc.Enabled,
	)
	return scanService(row, r.cipher, true)
}

func (r *ServiceRepo) Delete(ctx context.Context, id string) error {
	uid, err := uuidArg(id)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM services WHERE id = $1`, uid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// uuidArg converts a UUID string into a pgtype.UUID for use as a query
// parameter. pgx v5 does not encode a Go string into a uuid column directly.
func uuidArg(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

func scanService(row pgx.Row, c *crypto.Cipher, withSecrets bool) (model.Service, error) {
	var svc model.Service
	var encKey, encPass string
	err := row.Scan(
		&svc.ID, &svc.Slug, &svc.Name, &svc.UpstreamURL, &encKey, &svc.AuthHeader, &svc.AuthScheme,
		&svc.ProxyHost, &svc.ProxyPort, &svc.ProxyUsername, &encPass, &svc.Model, &svc.Enabled, &svc.CreatedAt, &svc.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Service{}, ErrNotFound
		}
		return model.Service{}, err
	}
	svc.HasAPIKey = encKey != ""
	svc.HasProxyPassword = encPass != ""
	if withSecrets {
		if svc.UpstreamAPIKey, err = c.Decrypt(encKey); err != nil {
			return model.Service{}, err
		}
		if svc.ProxyPassword, err = c.Decrypt(encPass); err != nil {
			return model.Service{}, err
		}
	}
	return svc, nil
}
