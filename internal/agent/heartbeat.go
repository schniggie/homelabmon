package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dx111ge/homelabmon/internal/diag"
	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/rs/zerolog/log"
)

const DefaultHeartbeatInterval = 60 * time.Second

// HeartbeatService sends periodic heartbeats to all known peers.
type HeartbeatService struct {
	identity        *models.NodeIdentity
	collector       *Collector
	store           *store.Store
	client          *http.Client
	interval        time.Duration
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	useTLS          bool
	scanCoordinator *ScanCoordinator
}

func NewHeartbeatService(identity *models.NodeIdentity, collector *Collector, s *store.Store) *HeartbeatService {
	return &HeartbeatService{
		identity:  identity,
		collector: collector,
		store:     s,
		client:    &http.Client{Timeout: 10 * time.Second},
		interval:  DefaultHeartbeatInterval,
	}
}

// SetScanCoordinator sets the scan coordinator for heartbeat scan-time exchange.
func (h *HeartbeatService) SetScanCoordinator(sc *ScanCoordinator) {
	h.scanCoordinator = sc
}

// SetTLSConfig configures the heartbeat client for mTLS.
func (h *HeartbeatService) SetTLSConfig(cfg *tls.Config) {
	h.client.Transport = &http.Transport{TLSClientConfig: cfg}
	h.useTLS = true
}

func (h *HeartbeatService) Start(ctx context.Context) {
	ctx, h.cancel = context.WithCancel(ctx)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		// Short delay to let collector get first reading
		time.Sleep(5 * time.Second)
		h.sendHeartbeats(ctx)

		ticker := time.NewTicker(h.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.sendHeartbeats(ctx)
			}
		}
	}()
	log.Info().Dur("interval", h.interval).Msg("heartbeat service started")
}

func (h *HeartbeatService) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	h.wg.Wait()
}

func (h *HeartbeatService) sendHeartbeats(ctx context.Context) {
	peers, err := h.store.ListPeers(ctx)
	if err != nil {
		log.Error().Err(err).Msg("failed to list peers for heartbeat")
		return
	}
	if len(peers) == 0 {
		return
	}

	host, _ := h.store.GetHost(ctx, h.identity.ID)

	// Include active services: named services + docker + fingerprinted.
	// Exclude unnamed port scan results (name="unknown").
	allSvcs, _ := h.store.ListServicesByHost(ctx, h.identity.ID)
	var svcs []models.DiscoveredService
	for _, s := range allSvcs {
		if s.Status != "active" {
			continue
		}
		if s.Category == "unknown" && s.Name == "unknown" {
			continue
		}
		svcs = append(svcs, s)
	}

	// Build known peer addresses for gossip
	var knownPeers []models.PeerAddr
	for _, p := range peers {
		knownPeers = append(knownPeers, models.PeerAddr{
			ID:      p.ID,
			Address: p.Address,
			Site:    p.Site,
		})
	}

	hb := models.Heartbeat{
		NodeID:     h.identity.ID,
		Hostname:   h.identity.Hostname,
		Address:    h.identity.BindAddr,
		Version:    h.identity.Version,
		Site:       h.identity.Site,
		Timestamp:  time.Now().UTC(),
		Host:       host,
		Metric:     h.collector.Latest(),
		Services:   svcs,
		KnownPeers: knownPeers,
	}
	if h.scanCoordinator != nil {
		hb.LastScanTime = h.scanCoordinator.LocalScanTime()
	}

	body, _ := json.Marshal(hb)

	for _, peer := range peers {
		go h.sendToPeer(ctx, peer, body)
	}
}

func (h *HeartbeatService) sendToPeer(ctx context.Context, peer models.PeerInfo, body []byte) {
	scheme := "http"
	if h.useTLS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s/api/v1/heartbeat", scheme, peer.Address)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("peer", peer.Hostname).Msg("heartbeat send failed")
		diag.Record("heartbeat_fail", "heartbeat to "+peer.Hostname+" failed",
			"peer", peer.Hostname, "addr", peer.Address, "error", err.Error())
		h.store.UpdateHostStatus(ctx, peer.ID, "offline")
		return
	}
	defer resp.Body.Close()

	var peerHB models.Heartbeat
	if err := json.NewDecoder(resp.Body).Decode(&peerHB); err != nil {
		return
	}

	// Store peer's host info, metrics, and services
	if peerHB.Host != nil {
		peerHB.Host.Status = "online"
		h.store.UpsertHost(ctx, peerHB.Host)
	}
	if peerHB.Metric != nil {
		h.store.InsertMetric(ctx, peerHB.Metric)
	}
	for _, svc := range peerHB.Services {
		h.store.UpsertService(ctx, &svc)
	}

	// Update peer status
	now := time.Now().UTC()
	h.store.UpsertPeer(ctx, &models.PeerInfo{
		ID:            peerHB.NodeID,
		Hostname:      peerHB.Hostname,
		Address:       peer.Address,
		LastHeartbeat: &now,
		Status:        "online",
		Version:       peerHB.Version,
		Site:          peerHB.Site,
	})

	// Record peer's last scan time for scan dedup
	if h.scanCoordinator != nil && peerHB.LastScanTime != nil {
		h.scanCoordinator.RecordPeerScan(peerHB.NodeID, *peerHB.LastScanTime)
	}

	// Gossip: auto-add unknown peers discovered via this peer
	for _, gp := range peerHB.KnownPeers {
		if gp.ID == h.identity.ID || gp.ID == peerHB.NodeID {
			continue // skip self and the peer we just talked to
		}
		existing, _ := h.store.GetPeer(ctx, gp.ID)
		if existing == nil {
			h.store.UpsertPeer(ctx, &models.PeerInfo{
				ID:      gp.ID,
				Address: gp.Address,
				Status:  "unknown",
				Site:    gp.Site,
			})
			log.Info().Str("peer_id", gp.ID[:8]).Str("addr", gp.Address).Str("via", peer.Hostname).Msg("discovered peer via gossip")
		}
	}

	log.Debug().Str("peer", peer.Hostname).Msg("heartbeat exchanged")
}
