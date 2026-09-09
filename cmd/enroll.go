package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll",
	Short: "Enroll this node with a CA node (one-shot; does not start the agent)",
	Long: `Generates this node's identity and key material, enrolls with the CA node
using a one-time enrollment token, and exits. The agent can afterwards be
started as a service without any enrollment flags -- the certs persist.

Pass the token via --enroll-token, or with --enroll-token - to read it from
stdin (keeps the token out of shell history and process lists).`,
	RunE: runEnroll,
}

func init() {
	rootCmd.AddCommand(enrollCmd)
}

func runEnroll(cmd *cobra.Command, args []string) error {
	dir := dataDir()
	enrollURL := viper.GetString("enroll-url")
	token := viper.GetString("enroll-token")
	if enrollURL == "" {
		return fmt.Errorf("--enroll-url is required")
	}
	if token == "" {
		return fmt.Errorf("--enroll-token is required")
	}
	if token == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read token from stdin: %w", err)
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			return fmt.Errorf("no enrollment token received on stdin")
		}
	}

	nodeID := loadOrCreateNodeID(dir)
	if err := enrollWithCA(dir, nodeID, enrollURL, token); err != nil {
		return fmt.Errorf("enrollment failed: %w", err)
	}

	// Add the CA node as first peer so heartbeats have a destination (the
	// service later starts without any enrollment flags).
	if st, err := store.New(dir); err != nil {
		return fmt.Errorf("open store: %w", err)
	} else {
		defer st.Close()
		if host := enrollHost(enrollURL); host != "" {
			now := time.Now().UTC()
			if err := st.UpsertPeer(context.Background(), &models.PeerInfo{
				ID:            "pending-" + host,
				Address:       host,
				Status:        "unknown",
				LastHeartbeat: &now,
			}); err != nil {
				return fmt.Errorf("add CA as initial peer: %w", err)
			}
		}
	}

	fmt.Printf("Enrolled successfully as node %s.\n", nodeID)
	fmt.Println("Certs saved to the data dir; start the agent without --enroll flags (e.g. via a systemd service).")
	return nil
}
