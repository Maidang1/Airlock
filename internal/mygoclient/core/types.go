package core

import (
	"encoding/json"
	"time"
)

type ControllerIdentity struct {
	Protocol int    `json:"protocol,omitempty"`
	ID       string `json:"id,omitempty"`
}

type DesktopResult struct {
	Status     string             `json:"status"`
	URL        string             `json:"url,omitempty"`
	Message    string             `json:"message,omitempty"`
	Legacy     bool               `json:"legacy,omitempty"`
	Controller ControllerIdentity `json:"controller"`
}

type HostKey struct {
	Algorithm   string `json:"algorithm,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type HostAuthConfig struct {
	Kind    string `json:"kind"`
	KeyPath string `json:"key_path,omitempty"`
}

type HostProfile struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Host       string         `json:"host"`
	Port       int            `json:"port"`
	User       string         `json:"user"`
	Platform   string         `json:"platform,omitempty"`
	Auth       HostAuthConfig `json:"auth"`
	HostKey    *HostKey       `json:"host_key,omitempty"`
	Executable string         `json:"executable,omitempty"`
	ConfigPath string         `json:"config_path,omitempty"`
}

type PublicManifest struct {
	Platform string   `json:"platform,omitempty"`
	Version  string   `json:"version,omitempty"`
	Features []string `json:"features,omitempty"`
}

type ConnectionState struct {
	State        string          `json:"state"`
	ConnectionID string          `json:"connection_id,omitempty"`
	HostID       string          `json:"host_id,omitempty"`
	InstanceID   string          `json:"instance_id,omitempty"`
	Phase        string          `json:"phase,omitempty"`
	Message      string          `json:"message,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	HostKey      *HostKey        `json:"host_key,omitempty"`
	Manifest     *PublicManifest `json:"manifest,omitempty"`
}

type DesktopCandidate struct {
	ID         string          `json:"id"`
	Mode       string          `json:"mode"`
	Connection ConnectionState `json:"connection"`
}

type ClientContext struct {
	DesktopGateway  bool              `json:"desktop_gateway,omitempty"`
	Candidate       *DesktopCandidate `json:"candidate,omitempty"`
	ClientOnly      bool              `json:"client_only"`
	Mode            string            `json:"mode"`
	ClientPlatform  string            `json:"client_platform"`
	ConnectionID    string            `json:"connection_id"`
	State           ConnectionState   `json:"connection"`
	InstanceID      string            `json:"instance_id,omitempty"`
	HostID          string            `json:"host_id,omitempty"`
	HostFingerprint string            `json:"host_fingerprint,omitempty"`
	ServerPlatform  string            `json:"server_platform"`
	Name            string            `json:"name,omitempty"`
}

type DesktopLocalStatus struct {
	Available      bool   `json:"available"`
	Known          bool   `json:"known"`
	ServiceRunning bool   `json:"service_running"`
	Running        bool   `json:"running"`
	Busy           bool   `json:"busy"`
	LiveTerminals  int    `json:"live_terminals"`
	ControllerID   string `json:"controller_id,omitempty"`
	Message        string `json:"message,omitempty"`
}

type AccountView struct {
	RecoveryReady         bool              `json:"recovery_ready"`
	MacOSAccountHost      bool              `json:"macos_account_host,omitempty"`
	MacOSCredentialsReady bool              `json:"macos_credentials_ready,omitempty"`
	MacOSUsernameBound    bool              `json:"macos_username_bound,omitempty"`
	MacOSPasswordPending  bool              `json:"macos_password_pending,omitempty"`
	MacOSGuestUser        string            `json:"macos_guest_user,omitempty"`
	InitialMacSetup       bool              `json:"initial_mac_setup,omitempty"`
	GuestSystem           string            `json:"guest_system,omitempty"`
	TrustedLocal          bool              `json:"trusted_local,omitempty"`
	TrustedLogin          bool              `json:"trusted_login,omitempty"`
	Exists                bool              `json:"exists"`
	Unlocked              bool              `json:"unlocked"`
	Username              string            `json:"username"`
	Avatar                string            `json:"avatar,omitempty"`
	BackgroundRunning     bool              `json:"background_running"`
	DeviceBinding         string            `json:"device_binding"`
	Devices               map[string]string `json:"devices,omitempty"`
	RequireAuthentication bool              `json:"require_authentication"`
	RecoveryKey           string            `json:"recovery_key,omitempty"`
}

type InitializationView struct {
	Required bool `json:"required"`
	Started  bool `json:"started"`
	Tracked  bool `json:"tracked"`
	Prepared bool `json:"prepared"`
}

type SetupProgress struct {
	Phase          string  `json:"phase"`
	Label          string  `json:"label"`
	Received       *int64  `json:"received,omitempty"`
	Total          int64   `json:"total,omitempty"`
	System         string  `json:"system,omitempty"`
	Operation      string  `json:"operation,omitempty"`
	BytesPerSecond float64 `json:"bytes_per_second,omitempty"`
}

type Job struct {
	Canceled      bool           `json:"canceled,omitempty"`
	SettingsSaved bool           `json:"settings_saved,omitempty"`
	SetupProgress *SetupProgress `json:"setup_progress,omitempty"`
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Running       bool           `json:"running"`
	Message       string         `json:"message"`
	Error         string         `json:"error,omitempty"`
	Recovery      string         `json:"recovery,omitempty"`
	Workspace     string         `json:"workspace,omitempty"`
	Log           string         `json:"log,omitempty"`
}

type ProjectGroup struct {
	Workspace string   `json:"workspace"`
	Name      string   `json:"name"`
	Folders   []string `json:"folders"`
}

type StorageRecoveryView struct {
	Required      bool   `json:"required"`
	Error         string `json:"error,omitempty"`
	RepairAllowed bool   `json:"repair_allowed"`
}

type SafetyCheck struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Critical  bool      `json:"critical"`
	Evidence  string    `json:"evidence"`
	Remedy    string    `json:"remedy,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

type SafetyReport struct {
	Policy        string        `json:"policy"`
	ID            string        `json:"id"`
	RunID         string        `json:"run_id"`
	CheckID       string        `json:"check_id"`
	Configuration string        `json:"configuration"`
	CreatedAt     time.Time     `json:"created_at"`
	Checks        []SafetyCheck `json:"checks"`
}

type GateCounters struct {
	PublicBlocked       int `json:"public_blocked,omitempty"`
	ManagementBlocked   int `json:"management_blocked,omitempty"`
	UDPBlocked          int `json:"udp_blocked,omitempty"`
	IPv6Blocked         int `json:"ipv6_blocked,omitempty"`
	PublicDNSBlocked    int `json:"public_dns_blocked,omitempty"`
	UnauthorizedStopped int `json:"unauthorized_stopped,omitempty"`
}

type GateStatus struct {
	IPv6LinkProbeCheck string        `json:"ipv6_link_probe_check,omitempty"`
	Epoch              uint64        `json:"epoch"`
	PolicyVerified     bool          `json:"policy_verified"`
	Policy             string        `json:"policy"`
	RunID              string        `json:"run_id"`
	CheckID            string        `json:"check_id,omitempty"`
	CheckStartedAt     time.Time     `json:"check_started_at,omitempty"`
	Configuration      string        `json:"configuration"`
	Mode               string        `json:"mode"`
	Fault              bool          `json:"fault"`
	Until              time.Time     `json:"until"`
	Reason             string        `json:"reason,omitempty"`
	Counters           GateCounters  `json:"counters"`
	Connections        int           `json:"connections"`
	Report             *SafetyReport `json:"report,omitempty"`
}

type SafetyState struct {
	MacOSExitCandidate json.RawMessage `json:"macos_exit_candidate,omitempty"`
	Gate               *GateStatus     `json:"gate,omitempty"`
	Report             *SafetyReport   `json:"report,omitempty"`
	Error              string          `json:"error,omitempty"`
	ExitIP             string          `json:"exit_ip,omitempty"`
	VerifiedAt         time.Time       `json:"verified_at,omitempty"`
}

type LocationView struct {
	IP           string `json:"ip,omitempty"`
	Country      string `json:"country,omitempty"`
	CountryCode  string `json:"country_code,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
	Locale       string `json:"locale,omitempty"`
	Language     string `json:"language,omitempty"`
	AcceptLang   string `json:"accept_language,omitempty"`
	Organization string `json:"organization,omitempty"`
}

type WindowsCapabilities struct {
	Display bool `json:"display,omitempty"`
	Browser bool `json:"browser,omitempty"`
	Input   bool `json:"input,omitempty"`
	Audio   bool `json:"audio,omitempty"`
}

type StatusView struct {
	Projects                  []string             `json:"projects"`
	ProjectGroups             []ProjectGroup       `json:"project_groups"`
	Ready                     bool                 `json:"ready"`
	VMRunning                 bool                 `json:"vm_running"`
	Workspace                 string               `json:"workspace"`
	EnvironmentDirectory      string               `json:"environment_directory"`
	EnvironmentIdentityDir    string               `json:"environment_identity_directory"`
	LocalVMStorage            bool                 `json:"local_vm_storage"`
	ProjectError              string               `json:"project_error"`
	ProjectErrors             map[string]string    `json:"project_errors"`
	StorageRecovery           StorageRecoveryView  `json:"storage_recovery"`
	Keyboard                  string               `json:"keyboard"`
	AutoTimezone              bool                 `json:"auto_timezone"`
	Job                       Job                  `json:"job"`
	Location                  *LocationView        `json:"location,omitempty"`
	NetworkError              string               `json:"network_error"`
	CanRestore                bool                 `json:"can_restore"`
	Safety                    SafetyState          `json:"safety"`
	EnvironmentUpdateRequired bool                 `json:"environment_update_required"`
	EnvironmentUpdateError    string               `json:"environment_update_error"`
	GuestSystem               string               `json:"guest_system,omitempty"`
	GuestSystemError          string               `json:"guest_system_error,omitempty"`
	GuestCapabilities         *WindowsCapabilities `json:"guest_capabilities,omitempty"`
	WorkloadReady             *bool                `json:"workload_ready,omitempty"`
}

type ExitPolicy struct {
	CountryCode string `json:"country_code,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
	Language    string `json:"language,omitempty"`
	Locale      string `json:"locale,omitempty"`
}

type SettingsView struct {
	BalancedDisplay       bool       `json:"balanced_display"`
	ProjectFileCache      bool       `json:"project_file_cache"`
	MobileRelay           string     `json:"mobile_relay,omitempty"`
	Workspace             string     `json:"workspace"`
	BrowserHomeURL        string     `json:"browser_home_url"`
	ClaudeTerminalCommand string     `json:"claude_terminal_command"`
	Keyboard              string     `json:"keyboard"`
	Clock12Hour           bool       `json:"clock_12_hour"`
	AutoTimezone          bool       `json:"auto_timezone"`
	AutoLanguage          bool       `json:"auto_language"`
	DNSURL                string     `json:"dns_url"`
	Timezone              string     `json:"timezone"`
	Locale                string     `json:"locale"`
	BrowserLanguage       string     `json:"browser_language"`
	ExitPolicy            ExitPolicy `json:"exit_policy"`
}

type ProxyNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type ProxyNodeIssue struct {
	Index  int    `json:"index"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
	Reason string `json:"reason"`
}

type ProxySource struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Remote        bool             `json:"remote"`
	Host          string           `json:"host"`
	IntervalHours int              `json:"interval_hours"`
	UpdatedAt     time.Time        `json:"updated_at"`
	Error         string           `json:"error,omitempty"`
	Usage         string           `json:"usage,omitempty"`
	Nodes         []ProxyNode      `json:"nodes"`
	Skipped       []ProxyNodeIssue `json:"skipped,omitempty"`
}

type ProxyStatusView struct {
	Route         []string      `json:"route"`
	RouteError    string        `json:"route_error"`
	Sources       []ProxySource `json:"sources"`
	Selected      string        `json:"selected"`
	Port          int           `json:"port"`
	Running       bool          `json:"running"`
	Active        bool          `json:"active"`
	State         string        `json:"state"`
	StateLabel    string        `json:"state_label"`
	NetworkOpen   bool          `json:"network_open"`
	CoreAvailable bool          `json:"core_available"`
	Version       string        `json:"version"`
	Revision      string        `json:"revision"`
	BootstrapDNS  string        `json:"bootstrap_dns"`
	UDPBlocked    bool          `json:"udp_blocked"`
	IPv6Blocked   bool          `json:"ipv6_blocked"`
}

type BrowserTab struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Active    bool   `json:"active"`
	Incognito bool   `json:"incognito,omitempty"`
	Loading   bool   `json:"loading,omitempty"`
}

type BrowserUpload struct {
	ID       string `json:"id"`
	Multiple bool   `json:"multiple,omitempty"`
	Accept   string `json:"accept,omitempty"`
}

type BrowserDownload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Received  int64  `json:"received,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Path      string `json:"path,omitempty"`
	Target    string `json:"target,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
	Error     string `json:"error,omitempty"`
}

type BrowserTabsView struct {
	Ready     bool              `json:"ready"`
	Available bool              `json:"available"`
	Editor    string            `json:"editor,omitempty"`
	Tabs      []BrowserTab      `json:"tabs"`
	ActiveID  string            `json:"active_id,omitempty"`
	Error     string            `json:"error,omitempty"`
	Uploads   []BrowserUpload   `json:"uploads,omitempty"`
	Downloads []BrowserDownload `json:"downloads,omitempty"`
}

type TerminalSummary struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	Closed              bool   `json:"closed"`
	Workspace           string `json:"workspace"`
	Kind                string `json:"kind"`
	ClaudeAccountID     string `json:"claude_account_id,omitempty"`
	ClaudeAccountSource string `json:"claude_account_source,omitempty"`
	CLISessionID        string `json:"cli_session_id,omitempty"`
	CLIStarting         bool   `json:"cli_starting,omitempty"`
}

type CLISessionSummary struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Workspace       string `json:"workspace"`
	ProjectName     string `json:"project_name,omitempty"`
	ClaudeAccountID string `json:"claude_account_id,omitempty"`
	AccountLabel    string `json:"account_label,omitempty"`
	Status          string `json:"status,omitempty"`
	Archived        bool   `json:"archived,omitempty"`
	TerminalID      string          `json:"terminal_id,omitempty"`
	UpdatedAt       json.RawMessage `json:"updated_at,omitempty"`
	Running         bool            `json:"running,omitempty"`
}

type ClaudeWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
	ObservedAt     int64   `json:"observed_at,omitempty"`
}

type ClaudeQuota struct {
	FiveHour   *ClaudeWindow `json:"five_hour,omitempty"`
	SevenDay   *ClaudeWindow `json:"seven_day,omitempty"`
	ObservedAt int64         `json:"observed_at"`
}

type ClaudeScheduling struct {
	Mode         string `json:"mode"`
	SwitchPolicy string `json:"switch_policy"`
}

type ClaudeAccount struct {
	ID             string       `json:"id"`
	Label          string       `json:"label"`
	Source         string       `json:"source"`
	ProfileID      string       `json:"profile_id,omitempty"`
	Enabled        bool         `json:"enabled"`
	Status         string       `json:"status"`
	Email          string       `json:"email,omitempty"`
	Organization   string       `json:"organization,omitempty"`
	Plan           string       `json:"plan,omitempty"`
	HandoffGroup   string       `json:"handoff_group,omitempty"`
	Quota          *ClaudeQuota `json:"quota,omitempty"`
	CheckedAt      int64        `json:"checked_at,omitempty"`
	UsageStatus    string       `json:"usage_status,omitempty"`
	UsageCheckedAt int64        `json:"usage_checked_at,omitempty"`
	UsageChecking  bool         `json:"usage_checking,omitempty"`
}

type ClaudeDetected struct {
	Identity       string       `json:"identity"`
	Email          string       `json:"email"`
	Organization   string       `json:"organization,omitempty"`
	Plan           string       `json:"plan,omitempty"`
	Quota          *ClaudeQuota `json:"quota,omitempty"`
	CheckedAt      int64        `json:"checked_at"`
	UsageStatus    string       `json:"usage_status,omitempty"`
	UsageCheckedAt int64        `json:"usage_checked_at,omitempty"`
	UsageChecking  bool         `json:"usage_checking,omitempty"`
}

type ClaudePanelView struct {
	Scheduling             ClaudeScheduling `json:"scheduling"`
	UsagePanelRunning      bool             `json:"usage_panel_running"`
	UsageUnavailableReason string           `json:"usage_unavailable_reason,omitempty"`
	Accounts               []ClaudeAccount  `json:"accounts"`
	Detected               *ClaudeDetected  `json:"detected,omitempty"`
	DetectionStatus        string           `json:"detection_status"`
	DetectionError         string           `json:"detection_error,omitempty"`
	LoginNotice            string           `json:"login_notice,omitempty"`
}

type ClaudeDesktopState struct {
	Installed         bool    `json:"installed"`
	Version           string  `json:"version"`
	Running           bool    `json:"running"`
	Failed            bool    `json:"failed"`
	Managed           bool    `json:"managed"`
	NativeUserSession bool    `json:"native_user_session,omitempty"`
	DisplayScale      float64 `json:"display_scale,omitempty"`
	FailureReason     string  `json:"failure_reason,omitempty"`
}

type TailnetPeer struct {
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name"`
	Host     string   `json:"host,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	Online   bool     `json:"online,omitempty"`
	OS       string   `json:"os,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	ExitNode bool     `json:"exit_node,omitempty"`
}

type TailnetStatus struct {
	Installed bool          `json:"installed"`
	Connected bool          `json:"connected"`
	Backend   string        `json:"backend,omitempty"`
	SelfName  string        `json:"self_name,omitempty"`
	SelfIP    string        `json:"self_ip,omitempty"`
	AuthURL   string        `json:"auth_url,omitempty"`
	Peers     []TailnetPeer `json:"peers,omitempty"`
	Keys      []string      `json:"keys,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type VaultEntry struct {
	ID       string `json:"id"`
	Origin   string `json:"origin"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Updated  string `json:"updated"`
}

type VaultData struct {
	Entries []VaultEntry `json:"entries"`
	Blocked []string     `json:"blocked"`
}

type Favorite struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	FolderID string `json:"folder_id,omitempty"`
}

type FavoriteFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Favorites struct {
	Items   []Favorite       `json:"items"`
	Folders []FavoriteFolder `json:"folders"`
}

type AppearanceFonts struct {
	Sans string `json:"sans,omitempty"`
	Mono string `json:"mono,omitempty"`
}

type AppearanceTheme struct {
	Accent         string          `json:"accent,omitempty"`
	Surface        string          `json:"surface,omitempty"`
	SurfaceElevated string         `json:"surfaceElevated,omitempty"`
	Text           string          `json:"text,omitempty"`
	Muted          string          `json:"muted,omitempty"`
	Fonts          AppearanceFonts `json:"fonts,omitempty"`
}

type SavedAppearanceTheme struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Light AppearanceTheme `json:"light,omitempty"`
	Dark  AppearanceTheme `json:"dark,omitempty"`
}

type Appearance struct {
	CustomThemes   []SavedAppearanceTheme `json:"customThemes,omitempty"`
	Mode           string                 `json:"mode"`
	Light          AppearanceTheme        `json:"lightTheme"`
	Dark           AppearanceTheme        `json:"darkTheme"`
	UIFontSize     int                    `json:"uiFontSize"`
	CodeFontSize   int                    `json:"codeFontSize"`
	PointerCursors bool                   `json:"pointerCursors"`
	FontSmoothing  bool                   `json:"fontSmoothing"`
	ReducedMotion  string                 `json:"reducedMotion"`
}

type MobileDeviceInfo struct {
	Platform string `json:"platform"`
	Model    string `json:"model"`
}

type MobilePairStatus struct {
	Paired bool              `json:"paired"`
	Status string            `json:"status,omitempty"`
	Device *MobileDeviceInfo `json:"device,omitempty"`
	QR     string            `json:"qr,omitempty"`
	URL    string            `json:"url,omitempty"`
	Code   string            `json:"code,omitempty"`
	Pin    string            `json:"pin,omitempty"`
}

type DiagnosticsInfo struct {
	RetentionDays int   `json:"retention_days"`
	MaxBytes      int64 `json:"max_bytes"`
	GuestMaxBytes int64 `json:"guest_max_bytes"`
	Bytes         int64 `json:"bytes"`
	Files         int   `json:"files"`
}

type BuildInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit,omitempty"`
	Modified bool   `json:"modified"`
}

type AboutCheckItem struct {
	Status     string `json:"status"`
	Version    string `json:"version,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
	AheadBy    int    `json:"aheadBy,omitempty"`
	BehindBy   int    `json:"behindBy,omitempty"`
	Error      string `json:"error,omitempty"`
	RetryAt    string `json:"retryAt,omitempty"`
}

type AboutCheck struct {
	Build     BuildInfo      `json:"build"`
	Source    AboutCheckItem `json:"source"`
	Release   AboutCheckItem `json:"release"`
	CheckedAt string         `json:"checkedAt"`
}

type ResourceStatus struct {
	Running               bool    `json:"running"`
	Automatic             bool    `json:"automatic"`
	Runtime               string  `json:"runtime,omitempty"`
	MemoryGiB             int     `json:"memory_gib"`
	CPUs                  int     `json:"cpus"`
	MaxMemoryGiB          int     `json:"max_memory_gib"`
	MaxCPUs               int     `json:"max_cpus"`
	DiskGiB               int     `json:"disk_gib,omitempty"`
	MaxDiskGiB            int     `json:"max_disk_gib,omitempty"`
	RestartRequired       bool    `json:"restart_required,omitempty"`
	AllocationError       string  `json:"allocation_error,omitempty"`
	RecommendedMemBytes   int64   `json:"recommended_memory_bytes,omitempty"`
	ConfiguredMemBytes    int64   `json:"configured_memory_bytes,omitempty"`
	ConfiguredCPUs        int     `json:"configured_cpus,omitempty"`
	ResourcesVerified     bool    `json:"resources_verified,omitempty"`
	ResourceError         string  `json:"resource_error,omitempty"`
}

type DesktopFileRequest struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Name        string `json:"name,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Application string          `json:"application,omitempty"`
	RequestedAt json.RawMessage `json:"requested_at,omitempty"`
}

type BrowserEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Raw     map[string]any  `json:"-"`
}

type ClientPreferences struct {
	AudioMuted         bool    `json:"audio_muted"`
	AudioVolume        float64 `json:"audio_volume"`
	ThemeMode          string  `json:"theme_mode"` // "system" | "light" | "dark"
	SidebarWidth       float32 `json:"sidebar_width"`
	TerminalSplit      float32 `json:"terminal_split"`
	ShowDiagnostics    bool    `json:"show_diagnostics"`
	PreferredBrowser   string  `json:"preferred_browser,omitempty"`
	PreferredProxyPort int     `json:"preferred_proxy_port,omitempty"`
}
