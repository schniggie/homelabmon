package mesh

import (
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/rs/zerolog/log"
)

func (t *Transport) setupRoutes() {
	t.mux.HandleFunc("POST /api/v1/register", t.handleRegister)
	t.mux.HandleFunc("POST /api/v1/heartbeat", t.handleHeartbeat)
	t.mux.HandleFunc("GET /api/v1/status", t.handleStatus)
	t.mux.HandleFunc("GET /api/v1/peers", t.handleListPeers)
	t.mux.HandleFunc("GET /api/v1/hosts", t.handleListHosts)
	t.mux.HandleFunc("GET /api/v1/metrics/latest", t.handleLatestMetric)
	t.mux.HandleFunc("GET /api/v1/metrics/history", t.handleMetricHistory)
	t.mux.HandleFunc("GET /api/v1/services", t.handleListServices)
	t.mux.HandleFunc("GET /api/v1/services/{host_id}", t.handleHostServices)
	t.mux.HandleFunc("POST /api/v1/enroll", t.handleEnroll)
	t.mux.HandleFunc("POST /api/v1/docker/control", t.handleDockerControl)
	t.mux.HandleFunc("POST /api/v1/exec", t.handleExec)
}

type registerRequest struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	Address  string `json:"address"`
	Version  string `json:"version"`
}

func (t *Transport) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	now := time.Now().UTC()
	peer := &models.PeerInfo{
		ID:            req.NodeID,
		Hostname:      req.Hostname,
		Address:       req.Address,
		Version:       req.Version,
		Status:        "online",
		LastHeartbeat: &now,
	}

	if err := t.store.UpsertPeer(r.Context(), peer); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store failed"})
		return
	}

	log.Info().Str("peer", req.Hostname).Str("addr", req.Address).Msg("peer registered")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"node_id": t.identity.ID,
	})
}

func (t *Transport) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb models.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid heartbeat"})
		return
	}

	// Store peer's host info
	if hb.Host != nil {
		hb.Host.Status = "online"
		t.store.UpsertHost(r.Context(), hb.Host)
	}

	// Store peer's metrics
	if hb.Metric != nil {
		t.store.InsertMetric(r.Context(), hb.Metric)
	}

	// Store peer's services
	for i := range hb.Services {
		t.store.UpsertService(r.Context(), &hb.Services[i])
	}

	// Update peer record — use the sender's announced listening address,
	// falling back to the remote IP with the announced port.
	peerAddr := hb.Address
	if peerAddr == "" {
		peerAddr = r.RemoteAddr // legacy fallback
	} else if peerAddr[0] == ':' {
		// Sender only sent ":9600", prepend the remote IP
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		peerAddr = net.JoinHostPort(host, peerAddr[1:])
	}
	now := time.Now().UTC()
	t.store.UpsertPeer(r.Context(), &models.PeerInfo{
		ID:            hb.NodeID,
		Hostname:      hb.Hostname,
		Address:       peerAddr,
		LastHeartbeat: &now,
		Status:        "online",
		Version:       hb.Version,
		Site:          hb.Site,
	})

	// Record peer's last scan time for scan dedup
	if t.scanCoordinator != nil && hb.LastScanTime != nil {
		t.scanCoordinator.RecordPeerScan(hb.NodeID, *hb.LastScanTime)
	}

	// Gossip: auto-add unknown peers from the sender's known list.
	// Placeholder ("pending-") entries are bootstrap targets, not real
	// identities -- gossiping them would make nodes befriend themselves.
	for _, gp := range hb.KnownPeers {
		if gp.ID == t.identity.ID || gp.ID == hb.NodeID || strings.HasPrefix(gp.ID, "pending-") {
			continue
		}
		existing, _ := t.store.GetPeer(r.Context(), gp.ID)
		if existing == nil {
			t.store.UpsertPeer(r.Context(), &models.PeerInfo{
				ID:      gp.ID,
				Address: gp.Address,
				Status:  "unknown",
				Site:    gp.Site,
			})
			log.Info().Str("peer_id", gp.ID[:8]).Str("addr", gp.Address).Str("via", hb.Hostname).Msg("discovered peer via gossip")
		}
	}

	// Respond with our own heartbeat (including services and known peers for gossip)
	host, _ := t.store.GetHost(r.Context(), t.identity.ID)
	allSvcs, _ := t.store.ListServicesByHost(r.Context(), t.identity.ID)
	var mySvcs []models.DiscoveredService
	for _, s := range allSvcs {
		if s.Status != "active" {
			continue
		}
		if s.Category == "unknown" && s.Name == "unknown" {
			continue
		}
		mySvcs = append(mySvcs, s)
	}
	myPeers, _ := t.store.ListPeers(r.Context())
	var knownPeers []models.PeerAddr
	for _, p := range myPeers {
		knownPeers = append(knownPeers, models.PeerAddr{
			ID:      p.ID,
			Address: p.Address,
			Site:    p.Site,
		})
	}
	myHB := models.Heartbeat{
		NodeID:     t.identity.ID,
		Hostname:   t.identity.Hostname,
		Address:    t.identity.BindAddr,
		Version:    t.identity.Version,
		Site:       t.identity.Site,
		Timestamp:  time.Now().UTC(),
		Host:       host,
		Metric:     t.collector.Latest(),
		Services:   mySvcs,
		KnownPeers: knownPeers,
	}
	if t.scanCoordinator != nil {
		myHB.LastScanTime = t.scanCoordinator.LocalScanTime()
	}

	writeJSON(w, http.StatusOK, myHB)
}

func (t *Transport) handleStatus(w http.ResponseWriter, r *http.Request) {
	host, _ := t.store.GetHost(r.Context(), t.identity.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_id":  t.identity.ID,
		"hostname": t.identity.Hostname,
		"version":  t.identity.Version,
		"host":     host,
		"metric":   t.collector.Latest(),
	})
}

func (t *Transport) handleListPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := t.store.ListPeers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, peers)
}

func (t *Transport) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := t.store.ListHosts(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (t *Transport) handleLatestMetric(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host_id")
	if hostID == "" {
		hostID = t.identity.ID
	}
	metric, err := t.store.GetLatestMetric(r.Context(), hostID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no metrics"})
		return
	}
	writeJSON(w, http.StatusOK, metric)
}

func (t *Transport) handleMetricHistory(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host_id")
	if hostID == "" {
		hostID = t.identity.ID
	}
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if v, err := strconv.Atoi(h); err == nil {
			hours = v
		}
	}
	metrics, err := t.store.GetMetricHistory(r.Context(), hostID, hours)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (t *Transport) handleListServices(w http.ResponseWriter, r *http.Request) {
	services, err := t.store.ListAllServices(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, services)
}

func (t *Transport) handleHostServices(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("host_id")
	services, err := t.store.ListServicesByHost(r.Context(), hostID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, services)
}

// handleEnroll processes enrollment requests from new nodes.
// The node sends a CSR + enrollment token, and receives a signed cert + CA cert.
func (t *Transport) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if t.pki == nil || !t.pki.CAExists() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this node is not a CA"})
		return
	}

	var req struct {
		Token  string `json:"token"`
		NodeID string `json:"node_id"`
		CSR    string `json:"csr"` // PEM-encoded CSR
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	// Validate enrollment token
	storedToken, err := t.store.GetSetting(r.Context(), "enroll-token")
	if err != nil || storedToken == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no enrollment token configured"})
		return
	}
	if req.Token != storedToken {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid enrollment token"})
		return
	}

	// Decode and sign the CSR
	block, _ := pem.Decode([]byte(req.CSR))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid CSR"})
		return
	}

	signedCert, err := t.pki.SignCSR(block.Bytes, req.NodeID)
	if err != nil {
		log.Error().Err(err).Msg("failed to sign CSR")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "signing failed"})
		return
	}

	caCert, err := t.pki.CACertPEM()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "CA cert read failed"})
		return
	}

	// Invalidate the token after use (one-time)
	t.store.SetSetting(r.Context(), "enroll-token", "")

	log.Info().Str("node_id", req.NodeID).Msg("node enrolled via token")

	writeJSON(w, http.StatusOK, map[string]string{
		"cert":    string(signedCert),
		"ca_cert": string(caCert),
	})
}

func (t *Transport) handleDockerControl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ContainerID string `json:"container_id"`
		Action      string `json:"action"` // start, stop, restart
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Action != "start" && req.Action != "stop" && req.Action != "restart" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be start, stop, or restart"})
		return
	}
	if t.docker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "docker not available on this node"})
		return
	}
	if err := t.docker.ContainerAction(r.Context(), req.ContainerID, req.Action); err != nil {
		log.Warn().Err(err).Str("container", req.ContainerID).Str("action", req.Action).Msg("docker control failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	log.Info().Str("container", req.ContainerID).Str("action", req.Action).Msg("docker control success")
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
