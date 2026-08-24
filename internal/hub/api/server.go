package api

import (
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"time"

	"github.com/dx111ge/homelabmon/internal/agent"
	"github.com/dx111ge/homelabmon/internal/hub/llm"
	"github.com/dx111ge/homelabmon/internal/mesh"
	"github.com/dx111ge/homelabmon/internal/models"
	"github.com/dx111ge/homelabmon/internal/notify"
	"github.com/dx111ge/homelabmon/internal/store"
	"github.com/dx111ge/homelabmon/web"
)

// UIServer serves the web dashboard.
type UIServer struct {
	store        *store.Store
	collector    *agent.Collector
	identity     *models.NodeIdentity
	dispatcher   *notify.Dispatcher
	auth         *AuthManager
	scanEnabled  bool
	ScanFunc     func() (int, error) // triggers a network scan, returns device count
	PeerClient   *mesh.PeerClient    // routes management calls to the owning mesh node
	chatHandler  *llm.ChatHandler
	llmClient    *llm.Client
	dashTmpl     *template.Template
	hostTmpl     *template.Template
	svcsTmpl     *template.Template
	devsTmpl     *template.Template
	settingsTmpl *template.Template
	loginTmpl    *template.Template
}

func NewUIServer(s *store.Store, collector *agent.Collector, identity *models.NodeIdentity, scanEnabled bool, dispatcher *notify.Dispatcher, chatHandler *llm.ChatHandler, llmClient *llm.Client, auth *AuthManager) (*UIServer, error) {
	funcMap := template.FuncMap{
		"formatBytes": formatBytes,
		"formatPercent": func(p float64) string {
			return fmt.Sprintf("%.1f%%", p)
		},
		"percentColor": func(p float64) string {
			if p >= 90 {
				return "bg-red-500"
			}
			if p >= 70 {
				return "bg-yellow-500"
			}
			return "bg-green-500"
		},
		"clampPercent": func(p float64) float64 {
			return math.Min(math.Max(p, 0), 100)
		},
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "N/A"
			}
			return t.Format("2006-01-02 15:04")
		},
		"printf":     fmt.Sprintf,
		"svcIcon":    models.ServiceCategoryIcon,
		"deviceIcon": deviceTypeIcon,
		"hostLabel": func(h models.Host) string {
			return h.Label()
		},
		"splitServices": splitServices,
	}

	// Parse each page template separately with layout to avoid "content" name collisions
	dashTmpl, err := template.New("").Funcs(funcMap).ParseFS(web.TemplateFS, "templates/layout.html", "templates/dashboard.html")
	if err != nil {
		return nil, fmt.Errorf("parse dashboard templates: %w", err)
	}
	hostTmpl, err := template.New("").Funcs(funcMap).ParseFS(web.TemplateFS, "templates/layout.html", "templates/host.html")
	if err != nil {
		return nil, fmt.Errorf("parse host templates: %w", err)
	}
	svcsTmpl, err := template.New("").Funcs(funcMap).ParseFS(web.TemplateFS, "templates/layout.html", "templates/services.html")
	if err != nil {
		return nil, fmt.Errorf("parse services templates: %w", err)
	}
	devsTmpl, err := template.New("").Funcs(funcMap).ParseFS(web.TemplateFS, "templates/layout.html", "templates/devices.html")
	if err != nil {
		return nil, fmt.Errorf("parse devices templates: %w", err)
	}
	settingsTmpl, err := template.New("").Funcs(funcMap).ParseFS(web.TemplateFS, "templates/layout.html", "templates/settings.html")
	if err != nil {
		return nil, fmt.Errorf("parse settings templates: %w", err)
	}
	loginTmpl, err := template.New("").ParseFS(web.TemplateFS, "templates/login.html")
	if err != nil {
		return nil, fmt.Errorf("parse login template: %w", err)
	}

	return &UIServer{
		store:        s,
		collector:    collector,
		identity:     identity,
		dispatcher:   dispatcher,
		auth:         auth,
		scanEnabled:  scanEnabled,
		chatHandler:  chatHandler,
		llmClient:    llmClient,
		dashTmpl:     dashTmpl,
		hostTmpl:     hostTmpl,
		svcsTmpl:     svcsTmpl,
		devsTmpl:     devsTmpl,
		settingsTmpl: settingsTmpl,
		loginTmpl:    loginTmpl,
	}, nil
}

// SetupRoutes mounts UI routes on the given mux.
func (u *UIServer) SetupRoutes(mux *http.ServeMux) {
	// Static files (no auth needed)
	staticFS, _ := fs.Sub(web.StaticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Login routes (no auth needed)
	mux.HandleFunc("GET /ui/login", u.handleLoginPage)
	mux.HandleFunc("POST /ui/login/submit", u.handleLoginSubmit)
	mux.HandleFunc("POST /ui/logout", u.handleLogout)

	// Pages
	mux.HandleFunc("GET /", u.handleDashboard)
	mux.HandleFunc("GET /ui/dashboard-content", u.handleDashboardContent)
	mux.HandleFunc("GET /ui/host/{id}", u.handleHostDetail)
	mux.HandleFunc("GET /ui/host/{id}/metrics", u.handleHostMetrics)
	mux.HandleFunc("GET /api/v1/hosts/{id}/history", u.handleHostHistory)
	mux.HandleFunc("GET /ui/services", u.handleServicesPage)
	mux.HandleFunc("GET /ui/devices", u.handleDevicesPage)
	mux.HandleFunc("GET /ui/devices-content", u.handleDevicesContent)
	mux.HandleFunc("GET /ui/settings", u.handleSettingsPage)
	mux.HandleFunc("POST /ui/test-notification", u.handleTestNotification)
	mux.HandleFunc("POST /api/v1/settings", u.handleSaveSettings)
	mux.HandleFunc("POST /api/v1/scan", u.handleTriggerScan)

	// Chat (LLM) - sidebar panel, no dedicated page needed
	mux.HandleFunc("POST /api/v1/llm/chat", u.handleLLMChat)
	mux.HandleFunc("GET /api/v1/llm/status", u.handleLLMStatus)
	mux.HandleFunc("POST /api/v1/llm/clear", u.handleLLMClear)
	mux.HandleFunc("GET /api/v1/llm/sessions", u.handleLLMSessions)
	mux.HandleFunc("GET /api/v1/llm/sessions/{id}/messages", u.handleLLMSessionMessages)
	mux.HandleFunc("DELETE /api/v1/llm/sessions/{id}", u.handleLLMDeleteSession)

	// Debug (auth-protected; not a mesh route)
	mux.HandleFunc("GET /api/v1/debug/diagnostics", u.handleDebugDiagnostics)

	// Host management
	mux.HandleFunc("POST /api/v1/hosts/{id}/rename", u.handleRenameHost)
	mux.HandleFunc("POST /api/v1/hosts/{id}/type", u.handleUpdateDeviceType)
	mux.HandleFunc("DELETE /api/v1/hosts/{id}", u.handleDeleteHost)
	mux.HandleFunc("POST /api/v1/oui-check", u.handleOUICheck)

	// Peer management (auth-protected)
	mux.HandleFunc("DELETE /api/v1/peers/{id}", u.handleDeletePeer)

	// Docker control (proxied to agent)
	mux.HandleFunc("POST /ui/docker/control", u.handleDockerControl)

	// Integrations
	mux.HandleFunc("POST /api/v1/integrations", u.handleSaveIntegration)
	mux.HandleFunc("DELETE /api/v1/integrations/{id}", u.handleDeleteIntegration)
	mux.HandleFunc("POST /api/v1/integrations/{id}/test", u.handleTestIntegration)
	mux.HandleFunc("POST /api/v1/integrations/{id}/sync", u.handleSyncIntegration)
}

func deviceTypeIcon(deviceType string) string {
	switch deviceType {
	case "phone":
		return "fa-solid fa-mobile-screen"
	case "tablet":
		return "fa-solid fa-tablet-screen-button"
	case "tv":
		return "fa-solid fa-tv"
	case "media":
		return "fa-solid fa-music"
	case "printer":
		return "fa-solid fa-print"
	case "camera":
		return "fa-solid fa-video"
	case "router", "switch", "ap":
		return "fa-solid fa-network-wired"
	case "nas":
		return "fa-solid fa-hard-drive"
	case "iot":
		return "fa-solid fa-microchip"
	case "server":
		return "fa-solid fa-server"
	case "desktop":
		return "fa-solid fa-desktop"
	case "laptop":
		return "fa-solid fa-laptop"
	default:
		return "fa-solid fa-circle-question"
	}
}

// serviceStack groups services by their Docker Compose stack name.
type serviceStack struct {
	Name     string
	Services []models.DiscoveredService
}

// splitServices separates Docker container services (grouped by stack) from system services.
type splitServicesResult struct {
	DockerStacks   []serviceStack
	DockerCount    int
	SystemServices []models.DiscoveredService
}

func splitServices(services []models.DiscoveredService) splitServicesResult {
	var result splitServicesResult
	order := []string{}
	groups := map[string][]models.DiscoveredService{}
	for _, svc := range services {
		if svc.Source == "docker" {
			name := svc.Stack
			if name == "" {
				name = "_ungrouped"
			}
			if _, exists := groups[name]; !exists {
				order = append(order, name)
			}
			groups[name] = append(groups[name], svc)
			result.DockerCount++
		} else {
			result.SystemServices = append(result.SystemServices, svc)
		}
	}
	for _, name := range order {
		result.DockerStacks = append(result.DockerStacks, serviceStack{Name: name, Services: groups[name]})
	}
	return result
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
