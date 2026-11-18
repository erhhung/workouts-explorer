package worker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func executionPodName() string { name, _ := os.Hostname(); return name }
func executionPodUID() string  { return os.Getenv("POD_UID") }

type AccountDeletionRunner struct {
	db       *pgxpool.Pool
	logger   *slog.Logger
	workerID string
	disposed func(context.Context, string, string) (bool, error)
}

func NewAccountDeletionRunner(db *pgxpool.Pool, logger *slog.Logger) *AccountDeletionRunner {
	return &AccountDeletionRunner{db: db, logger: logger, workerID: "account-purge-" + uuid.NewString(), disposed: disposedPodVerifier()}
}

func (r *AccountDeletionRunner) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		worked, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.logger.WarnContext(ctx, "account deletion cycle deferred", "code", "purge-retry-needed")
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	return nil
}

func (r *AccountDeletionRunner) RunOnce(ctx context.Context) (bool, error) {
	lease := uuid.New()
	var job *uuid.UUID
	if err := r.db.QueryRow(ctx, `SELECT app.claim_account_deletion($1,$2,$3)`, r.workerID, lease, database.SupportedSchemaVersion).Scan(&job); err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	stepCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	code := ""
	defer func() {
		releaseCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		var value any
		if code != "" {
			value = code
		}
		_, _ = r.db.Exec(releaseCtx, `SELECT app.release_account_deletion($1,$2,$3,$4)`, *job, r.workerID, lease, value)
	}()
	rows, err := r.db.Query(stepCtx, `SELECT pod_name,pod_uid FROM app.account_deletion_staging($1,$2,$3)`, *job, r.workerID, lease)
	if err != nil {
		code = "purge-retry-needed"
		return true, err
	}
	type pod struct{ name, uid string }
	var pods []pod
	for rows.Next() {
		var p pod
		if err = rows.Scan(&p.name, &p.uid); err != nil {
			break
		}
		pods = append(pods, p)
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		code = "purge-retry-needed"
		return true, errors.New("staging receipt read failed")
	}
	for _, p := range pods {
		gone, verifyErr := r.disposed(stepCtx, p.name, p.uid)
		if verifyErr != nil {
			code = "staging-verification-unavailable"
			return true, verifyErr
		}
		if gone {
			if _, err = r.db.Exec(stepCtx, `SELECT app.ack_disposed_staging_pod($1,$2,$3,$4,$5)`, *job, r.workerID, lease, p.name, p.uid); err != nil {
				code = "purge-retry-needed"
				return true, err
			}
		}
	}
	tx, err := r.db.Begin(stepCtx)
	if err != nil {
		code = "purge-retry-needed"
		return true, err
	}
	defer tx.Rollback(stepCtx)
	if _, err = tx.Exec(stepCtx, `SET LOCAL lock_timeout='2s'`); err != nil {
		code = "purge-retry-needed"
		return true, err
	}
	var phase string
	if err = tx.QueryRow(stepCtx, `SELECT app.step_account_deletion($1,$2,$3)`, *job, r.workerID, lease).Scan(&phase); err != nil {
		code = "purge-retry-needed"
		return true, err
	}
	if err = tx.Commit(stepCtx); err != nil {
		code = "purge-retry-needed"
		return true, err
	}
	if phase == "completed" {
		r.logger.InfoContext(ctx, "account deletion completed", "job_id", *job)
	}
	return phase == "purging" || phase == "completed", nil
}

// Only disappearance/replacement of the exact pod UID proves its chart-owned
// emptyDir disposed. A live/terminating pod, denied API call or network fault is
// not cleanup evidence. This client has get-pods permission only, never secrets.
func disposedPodVerifier() func(context.Context, string, string) (bool, error) {
	host, namespace := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("POD_NAMESPACE")
	if host == "" || namespace == "" {
		return func(context.Context, string, string) (bool, error) { return false, nil }
	}
	return func(ctx context.Context, name, uid string) (bool, error) {
		token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
		if err != nil {
			return false, errors.New("pod disposal verification unavailable")
		}
		ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
		if err != nil {
			return false, errors.New("pod disposal verification unavailable")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return false, errors.New("pod disposal verification unavailable")
		}
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
		defer client.CloseIdleConnections()
		port := os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS")
		if port == "" {
			port = "443"
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(host, port)+"/api/v1/namespaces/"+url.PathEscape(namespace)+"/pods/"+url.PathEscape(name), nil)
		if err != nil {
			return false, errors.New("pod disposal verification unavailable")
		}
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		response, err := client.Do(request)
		if err != nil {
			return false, errors.New("pod disposal verification unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode == 404 {
			return true, nil
		}
		if response.StatusCode != 200 {
			return false, errors.New("pod disposal verification unavailable")
		}
		var pod struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&pod) != nil {
			return false, errors.New("pod disposal verification unavailable")
		}
		return pod.Metadata.UID != "" && pod.Metadata.UID != uid, nil
	}
}
