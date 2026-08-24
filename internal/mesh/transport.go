package mesh

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/dx111ge/homelabmon/internal/agent"
	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/rs/zerolog/log"
)

// DockerController can start/stop/restart containers.
type DockerController interface {
	ContainerAction(ctx context.Context, containerID, action string) error
}

// Transport handles HTTP communication between mesh nodes.
type Transport struct {
	identity        *models.NodeIdentity
	store           *store.Store
	collector       *agent.Collector
	server          *http.Server
	mux             *http.ServeMux
	handler         http.Handler
	pki             *PKI
	docker          DockerController
	scanCoordinator *agent.ScanCoordinator
	execEnabled     bool
}

// SetScanCoordinator sets the scan coordinator for heartbeat scan-time exchange.
func (t *Transport) SetScanCoordinator(sc *agent.ScanCoordinator) {
	t.scanCoordinator = sc
}

func NewTransport(identity *models.NodeIdentity, s *store.Store, collector *agent.Collector) *Transport {
	t := &Transport{
		identity:  identity,
		store:     s,
		collector: collector,
		mux:       http.NewServeMux(),
	}
	t.setupRoutes()
	return t
}

// Mux returns the HTTP mux so the UI server can mount additional routes.
func (t *Transport) Mux() *http.ServeMux {
	return t.mux
}

// SetHandler allows wrapping the mux with middleware (e.g., auth).
func (t *Transport) SetHandler(h http.Handler) {
	t.handler = h
}

// SetDocker sets the Docker controller for container management.
func (t *Transport) SetDocker(d DockerController) {
	t.docker = d
}

// SetPKI configures TLS for the transport.
func (t *Transport) SetPKI(pki *PKI) {
	t.pki = pki
}

func (t *Transport) Start(bindAddr string) error {
	handler := http.Handler(t.mux)
	if t.handler != nil {
		handler = t.handler
	}
	t.server = &http.Server{
		Addr:         bindAddr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if t.pki != nil && t.pki.Ready() {
		t.server.TLSConfig = t.pki.ServerTLSConfig()
		// Also allow non-mTLS connections (browsers, enrollment)
		// by accepting but not requiring client certs
		t.server.TLSConfig.ClientAuth = tls.VerifyClientCertIfGiven
		go func() {
			log.Info().Str("bind", bindAddr).Msg("transport listening (mTLS)")
			if err := t.server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatal().Err(err).Msg("transport failed")
			}
		}()
	} else {
		go func() {
			log.Info().Str("bind", bindAddr).Msg("transport listening")
			if err := t.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal().Err(err).Msg("transport failed")
			}
		}()
	}
	return nil
}

func (t *Transport) Stop(ctx context.Context) error {
	if t.server != nil {
		return t.server.Shutdown(ctx)
	}
	return nil
}
