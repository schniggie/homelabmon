package mesh

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/store"
)

// PeerClient performs management calls against nodes in the mesh.
// Calls for the local node are routed via loopback; calls for other nodes
// are routed to the peer's announced address (over mTLS when configured).
type PeerClient struct {
	identity *models.NodeIdentity
	store    *store.Store
	http     *http.Client
	tlsCfg   *tls.Config
}

func NewPeerClient(identity *models.NodeIdentity, s *store.Store) *PeerClient {
	return &PeerClient{
		identity: identity,
		store:    s,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// SetTLSConfig enables HTTPS with client certificates for peer calls.
func (c *PeerClient) SetTLSConfig(cfg *tls.Config) {
	c.tlsCfg = cfg
	c.http.Transport = &http.Transport{TLSClientConfig: cfg}
}

// DockerControl starts, stops, or restarts a container on the given host.
func (c *PeerClient) DockerControl(ctx context.Context, hostID, containerID, action string) error {
	url, err := c.nodeURL(hostID, "/api/v1/docker/control")
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, url, map[string]string{
		"container_id": containerID,
		"action":       action,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("node returned status %d", resp.StatusCode)
	}
	return nil
}

// Exec runs a command on the given host (loopback for the local node, the
// peer's address otherwise) and returns its result.
func (c *PeerClient) Exec(ctx context.Context, hostID, command, shell string, timeoutSec int) (*ExecResult, error) {
	url, err := c.nodeURL(hostID, "/api/v1/exec")
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, url, map[string]interface{}{
		"command":         command,
		"shell":           shell,
		"timeout_seconds": timeoutSec,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var result ExecResult
	json.Unmarshal(body, &result)
	if resp.StatusCode >= 400 {
		var errBody struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &errBody)
		msg := errBody.Error
		if msg == "" {
			msg = fmt.Sprintf("node returned status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return &result, nil
}

// PostJSON sends a JSON POST to the node owning hostID and decodes the
// response body into out (which may be nil to discard it).
func (c *PeerClient) PostJSON(ctx context.Context, hostID, path string, payload interface{}, out interface{}) error {
	url, err := c.nodeURL(hostID, path)
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, url, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
		}
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("node returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *PeerClient) post(ctx context.Context, url string, payload interface{}) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("node unreachable: %w", err)
	}
	return resp, nil
}

// nodeURL resolves the base URL for a host: loopback for the local node,
// the peer's announced address otherwise.
func (c *PeerClient) nodeURL(hostID, path string) (string, error) {
	scheme := "http"
	if c.tlsCfg != nil {
		scheme = "https"
	}

	if hostID == "" || hostID == c.identity.ID {
		return fmt.Sprintf("%s://%s%s", scheme, c.localAddr(), path), nil
	}

	peer, err := c.store.GetPeer(context.Background(), hostID)
	if err != nil || peer == nil {
		return "", fmt.Errorf("no mesh peer known for host %s", shortID(hostID))
	}
	if peer.Address == "" {
		return "", fmt.Errorf("peer %s has no address", shortID(hostID))
	}
	return fmt.Sprintf("%s://%s%s", scheme, peer.Address, path), nil
}

// localAddr turns the bind address (e.g. ":9600" or "0.0.0.0:9600") into a
// loopback host:port so management calls never leave the node.
func (c *PeerClient) localAddr() string {
	bind := c.identity.BindAddr
	if bind == "" {
		bind = ":9600"
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return bind
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
