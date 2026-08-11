package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/swaggest/swgui/v5emb"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

//go:embed openapi.yaml
var openAPIDocument []byte

type Server struct {
	config     config.API
	db         *pgxpool.Pool
	tileClient *http.Client
	swagger    http.Handler
	passwords  *passwordHasher
	delivery   *deliveryService
	avatars    *avatarService
	sourceKeys *sourcecrypto.Keyring
}

func NewHandler(cfg config.API, db *pgxpool.Pool, logger *slog.Logger) (http.Handler, error) {
	return NewHandlerContext(context.Background(), cfg, db, logger)
}

func NewHandlerContext(ctx context.Context, cfg config.API, db *pgxpool.Pool, logger *slog.Logger) (http.Handler, error) {
	spec, err := generated.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("load embedded OpenAPI document: %w", err)
	}
	spec.Servers = nil

	swagger := v5emb.New("Workouts Explorer API", "/api/openapi.yaml", "/swagger")
	sender, err := newSMTPSender(cfg.SMTP)
	if err != nil {
		return nil, fmt.Errorf("configure SMTP: %w", err)
	}
	delivery := newDeliveryService(sender)
	go func() {
		<-ctx.Done()
		delivery.close()
	}()
	sourceKeys, err := sourcecrypto.LoadKeyring(cfg.SourceKeyringFile)
	if err != nil {
		delivery.close()
		return nil, fmt.Errorf("configure source encryption: %w", err)
	}
	tileClient := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	server := &Server{config: cfg, db: db, tileClient: tileClient, swagger: swagger, passwords: newPasswordHasher(cfg.PasswordMinimum), delivery: delivery, avatars: newAvatarService(), sourceKeys: sourceKeys}
	startSecurityMaintenance(ctx, db, logger)
	csp, err := swaggerContentSecurityPolicy(swagger)
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()
	router.Use(requestID)
	router.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http.request")
	})
	router.Use(accessLog(logger))
	router.Use(securityHeaders(csp))
	router.Handle("/swagger/*", swagger)

	validated := chi.NewRouter()
	validated.Use(requestBodyLimit(config.MaxRequestBodyBytes))
	validated.Use(requireJSONContentType)
	validated.Use(requireValidJSONSyntax)
	validated.Use(nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			// Handlers apply bearer precedence and role checks after contract validation.
			AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil },
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, r *http.Request, options nethttpmiddleware.ErrorHandlerOpts) {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeProblem(w, r, http.StatusRequestEntityTooLarge, "Content Too Large", "request body exceeds the allowed size")
				return
			}
			writeProblem(w, r, options.StatusCode, http.StatusText(options.StatusCode), "request does not match the API contract")
		},
	}))
	generated.HandlerFromMux(server, validated)
	router.Mount("/", validated)
	return router, nil
}

func requireValidJSONSyntax(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPatch {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeProblem(w, r, http.StatusRequestEntityTooLarge, "Content Too Large", "request body exceeds the allowed size")
				return
			}
			writeProblem(w, r, http.StatusBadRequest, "Bad Request", "request does not match the API contract")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if !json.Valid(body) {
			writeProblem(w, r, http.StatusBadRequest, "Bad Request", "request does not match the API contract")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireJSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeProblem(w, r, http.StatusUnsupportedMediaType, "Unsupported Media Type", "request body must use application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) GetPublicConfig(w http.ResponseWriter, _ *http.Request) {
	passwordMinimum := s.config.PasswordMinimum
	if passwordMinimum == 0 {
		passwordMinimum = 12
	}
	pageSizeMaximum := s.config.PageSizeMaximum
	if pageSizeMaximum == 0 {
		pageSizeMaximum = 100
	}
	response := generated.PublicConfig{
		ProductName:            "Workouts Explorer",
		PollingIntervalSeconds: s.config.PollingIntervalSeconds,
		MapFitPaddingPixels:    s.config.MapFitPaddingPixels,
		PasswordMinimumLength:  passwordMinimum,
		PageSizeMaximum:        pageSizeMaximum,
		BaseMaps:               publicBaseMaps(s.config.BaseMaps),
	}
	writeJSON(w, http.StatusOK, response)
}

func publicBaseMaps(value config.BaseMaps) generated.BaseMapsConfig {
	if len(value.StyleFamilies) == 0 {
		value = config.DefaultBaseMaps()
	}
	result := generated.BaseMapsConfig{FallbackFamilyId: value.FallbackFamilyID, Families: make([]generated.BaseMapFamily, 0, len(value.StyleFamilies)), WorkoutTypeMappings: make([]generated.BaseMapWorkoutTypeMapping, 0, len(value.WorkoutTypeMappings))}
	for _, family := range value.StyleFamilies {
		item := generated.BaseMapFamily{
			Id: family.ID, Label: family.Label, ResourceOrigins: family.ResourceOrigins,
			Styles:      generated.BaseMapStyles{Light: family.Styles.Light, Dark: family.Styles.Dark},
			Attribution: generated.BaseMapAttribution{Text: family.Attribution.Text, Links: make([]generated.BaseMapAttributionLink, 0, len(family.Attribution.Links))},
		}
		for _, link := range family.Attribution.Links {
			item.Attribution.Links = append(item.Attribution.Links, generated.BaseMapAttributionLink{Label: link.Label, Url: link.URL})
		}
		result.Families = append(result.Families, item)
	}
	for _, mapping := range value.WorkoutTypeMappings {
		result.WorkoutTypeMappings = append(result.WorkoutTypeMappings, generated.BaseMapWorkoutTypeMapping{
			ProviderLabel: mapping.ProviderLabel, NormalizedTypeKey: mapping.NormalizedTypeKey, FamilyId: mapping.FamilyID,
		})
	}
	return result
}

func (*Server) GetOpenAPIDocument(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPIDocument)
}

func (s *Server) GetSwaggerUI(w http.ResponseWriter, r *http.Request) {
	s.swagger.ServeHTTP(w, r)
}

func (*Server) GetLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, generated.Health{Status: generated.Ok})
}

func (s *Server) GetReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if !database.Ready(ctx, s.db) {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "database schema is unavailable or incompatible")
		return
	}
	writeJSON(w, http.StatusOK, generated.Health{Status: generated.Ok})
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSON(w, status, generated.Problem{
		Type:      "about:blank",
		Title:     title,
		Status:    status,
		Detail:    &detail,
		RequestId: requestIDFrom(r.Context()),
	})
}

func writeValidationProblem(w http.ResponseWriter, r *http.Request, status int, detail string, fields ...generated.ValidationError) {
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSON(w, status, generated.Problem{Type: "https://workouts-explorer.invalid/problems/validation", Title: http.StatusText(status), Status: status, Detail: &detail, RequestId: requestIDFrom(r.Context()), Errors: &fields})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type requestIDKey struct{}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if _, err := uuid.Parse(id); err != nil {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func requestBodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				writeProblem(w, r, http.StatusRequestEntityTooLarge, "Content Too Large", "request body exceeds the allowed size")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

func securityHeaders(csp string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", csp)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}
}

func swaggerContentSecurityPolicy(swagger http.Handler) (string, error) {
	response := httptest.NewRecorder()
	swagger.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/swagger", nil))
	if response.Code != http.StatusOK {
		return "", fmt.Errorf("render embedded Swagger UI for CSP: status %d", response.Code)
	}
	scriptHashes := inlineHashes(response.Body.Bytes(), regexp.MustCompile(`(?s)<script>(.*?)</script>`))
	styleHashes := inlineHashes(response.Body.Bytes(), regexp.MustCompile(`(?s)<style>(.*?)</style>`))
	if len(scriptHashes) == 0 || len(styleHashes) == 0 {
		return "", fmt.Errorf("render embedded Swagger UI for CSP: inline assets not found")
	}
	return "default-src 'none'; script-src 'self' " + strings.Join(scriptHashes, " ") +
		"; style-src 'self' " + strings.Join(styleHashes, " ") +
		"; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'", nil
}

func inlineHashes(document []byte, expression *regexp.Regexp) []string {
	matches := expression.FindAllSubmatch(document, -1)
	hashes := make([]string, 0, len(matches))
	for _, match := range matches {
		sum := sha256.Sum256(bytes.Clone(match[1]))
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return hashes
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			if recorder.status >= http.StatusOK && recorder.status < http.StatusMultipleChoices &&
				(r.URL.Path == "/health/live" || r.URL.Path == "/health/ready") {
				return
			}
			attributes := []any{
				"request_id", requestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration_ms", time.Since(started).Milliseconds(),
			}
			spanContext := trace.SpanContextFromContext(r.Context())
			if spanContext.IsValid() {
				attributes = append(attributes, "trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String())
			}
			logger.InfoContext(r.Context(), "request completed", attributes...)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
