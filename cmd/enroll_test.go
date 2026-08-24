package cmd

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dx111ge/homelabmon/internal/mesh"
	"github.com/spf13/viper"
)

// TestEnrollWithCA runs the full enrollment flow against a TLS test server
// holding a real CA, including URL normalization (http + trailing slash).
func TestEnrollWithCA(t *testing.T) {
	caDir := t.TempDir()
	caPKI := mesh.NewPKI(caDir)
	if err := caPKI.GenerateCA(); err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	if err := caPKI.GenerateNodeCert("ca-node"); err != nil {
		t.Fatalf("generate CA node cert: %v", err)
	}
	if err := caPKI.Load(); err != nil {
		t.Fatalf("load CA: %v", err)
	}

	var gotToken, gotNodeID string
	enrollPathOK := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		enrollPathOK = true
		var req struct {
			Token  string `json:"token"`
			NodeID string `json:"node_id"`
			CSR    string `json:"csr"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotToken, gotNodeID = req.Token, req.NodeID

		if req.Token != "sekrit" {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})
			return
		}
		block, _ := pem.Decode([]byte(req.CSR))
		if block == nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "bad csr"})
			return
		}
		cert, err := caPKI.SignCSR(block.Bytes, req.NodeID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		caPEM, _ := caPKI.CACertPEM()
		json.NewEncoder(w).Encode(map[string]string{
			"cert":    string(cert),
			"ca_cert": string(caPEM),
		})
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	nodeDir := t.TempDir()
	// Deliberately: http scheme + trailing slash; enrollWithCA must normalize.
	url := strings.Replace(server.URL, "https://", "http://", 1) + "/"
	if err := enrollWithCA(nodeDir, "node-123", url, "sekrit"); err != nil {
		t.Fatalf("enrollWithCA: %v", err)
	}

	if !enrollPathOK {
		t.Error("enrollment request never hit POST /api/v1/enroll (URL normalization broken)")
	}
	if gotToken != "sekrit" || gotNodeID != "node-123" {
		t.Errorf("server got token=%q node_id=%q", gotToken, gotNodeID)
	}

	// Certs must be written and loadable, ready for mTLS
	nodePKI := mesh.NewPKI(nodeDir)
	if !nodePKI.CAExists() {
		t.Fatal("CA cert not saved on node")
	}
	if err := nodePKI.Load(); err != nil {
		t.Fatalf("node certs don't load: %v", err)
	}
	if !nodePKI.Ready() {
		t.Fatal("node PKI not ready after enrollment")
	}

	// Wrong token is rejected
	nodeDir2 := t.TempDir()
	url2 := strings.Replace(server.URL, "https://", "http://", 1)
	if err := enrollWithCA(nodeDir2, "node-456", url2, "wrong"); err == nil {
		t.Error("enrollment with wrong token must fail")
	}
}

func TestEnrollHost(t *testing.T) {
	cases := map[string]string{
		"https://192.168.178.199:9600":       "192.168.178.199:9600",
		"http://192.168.178.199:9600/":       "192.168.178.199:9600",
		"https://host.example:9600/whatever": "host.example:9600",
		"no-schema:9600":                     "no-schema:9600",
		"":                                   "",
		"notaurl":                            "",
	}
	for in, want := range cases {
		if got := enrollHost(in); got != want {
			t.Errorf("enrollHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEnrollFlagsBoundToViper guards against the original bug: the flags
// existed but were never bound, so viper.GetString returned "" and
// enrollment silently never ran.
func TestEnrollFlagsBoundToViper(t *testing.T) {
	defer rootCmd.PersistentFlags().Set("enroll-url", "")
	defer rootCmd.PersistentFlags().Set("enroll-token", "")

	if err := rootCmd.PersistentFlags().Set("enroll-url", "https://ca:9600"); err != nil {
		t.Fatal(err)
	}
	if err := rootCmd.PersistentFlags().Set("enroll-token", "tok"); err != nil {
		t.Fatal(err)
	}

	if got := viper.GetString("enroll-url"); got != "https://ca:9600" {
		t.Errorf("enroll-url not bound to viper, got %q", got)
	}
	if got := viper.GetString("enroll-token"); got != "tok" {
		t.Errorf("enroll-token not bound to viper, got %q", got)
	}
}
