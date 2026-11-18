package views

import (
	"context"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
	"github.com/sylvinus/agent-vm/internal/mygoclient/surfaces"
)

const (
	NavTerminal       = "terminal"
	NavBrowser        = "browser"
	NavClaudeDesktop  = "claude_desktop"
	NavProxy          = "proxy"
	NavSafety         = "safety"
	NavClaudeAccounts = "claude_accounts"
	NavTailscale      = "tailscale"
	NavRemoteHosts    = "remote_hosts"
	NavBrowserMgmt    = "browser_mgmt"
	NavSettings       = "settings"
)

type Palette struct {
	Dark           bool
	Bg             ui.Color
	SubtleBg       ui.Color
	SidebarBg      ui.Color
	CardBg         ui.Color
	ElevatedBg     ui.Color
	HoverBg        ui.Color
	ActiveNavBg    ui.Color
	Border         ui.Color
	Text           ui.Color
	Muted          ui.Color
	SubtleText     ui.Color
	Accent         ui.Color
	AccentSoft     ui.Color
	Success        ui.Color
	SuccessSoft    ui.Color
	Warning        ui.Color
	WarningSoft    ui.Color
	Danger         ui.Color
	DangerSoft     ui.Color
}

func PaletteFor(c *ui.Context) Palette {
	dark := false
	if c != nil && c.Theme() != nil {
		dark = c.Theme().Dark
	}
	if dark {
		return Palette{
			Dark:        true,
			Bg:          ui.Hex("#1e1e1e"),
			SubtleBg:    ui.Hex("#19191b"),
			SidebarBg:   ui.Hex("#232326"),
			CardBg:      ui.Hex("#28282c"),
			ElevatedBg:  ui.Hex("#323237"),
			HoverBg:     ui.RGBA(255, 255, 255, 0.06),
			ActiveNavBg: ui.RGBA(255, 255, 255, 0.11),
			Border:      ui.RGBA(255, 255, 255, 0.09),
			Text:        ui.Hex("#f5f5f7"),
			Muted:       ui.Hex("#98989d"),
			SubtleText:  ui.Hex("#636366"),
			Accent:      ui.Hex("#0a84ff"),
			AccentSoft:  ui.RGBA(10, 132, 255, 0.16),
			Success:     ui.Hex("#30d158"),
			SuccessSoft: ui.RGBA(48, 209, 88, 0.16),
			Warning:     ui.Hex("#ff9f0a"),
			WarningSoft: ui.RGBA(255, 159, 10, 0.16),
			Danger:      ui.Hex("#ff453a"),
			DangerSoft:  ui.RGBA(255, 69, 58, 0.16),
		}
	}
	return Palette{
		Dark:        false,
		Bg:          ui.Hex("#f5f5f7"),
		SubtleBg:    ui.Hex("#efeff4"),
		SidebarBg:   ui.Hex("#ebebf0"),
		CardBg:      ui.Hex("#ffffff"),
		ElevatedBg:  ui.Hex("#f2f2f7"),
		HoverBg:     ui.RGBA(0, 0, 0, 0.045),
		ActiveNavBg: ui.RGBA(0, 0, 0, 0.075),
		Border:      ui.RGBA(0, 0, 0, 0.085),
		Text:        ui.Hex("#1d1d1f"),
		Muted:       ui.Hex("#6e6e73"),
		SubtleText:  ui.Hex("#8e8e93"),
		Accent:      ui.Hex("#007aff"),
		AccentSoft:  ui.RGBA(0, 122, 255, 0.12),
		Success:     ui.Hex("#34c759"),
		SuccessSoft: ui.RGBA(52, 199, 89, 0.12),
		Warning:     ui.Hex("#ff9500"),
		WarningSoft: ui.RGBA(255, 149, 0, 0.12),
		Danger:      ui.Hex("#ff3b30"),
		DangerSoft:  ui.RGBA(255, 59, 48, 0.12),
	}
}

// RootView owns client-wide UI state across frames and delegates rendering to specialized panels.
type RootView struct {
	Store       *core.Store
	Client      *core.ControllerClient
	Bridge      core.HostBridge
	Audio       *core.AudioStream
	Clipboard   *core.ClipboardSync
	Devices     *core.AccountDeviceService
	Terminals   *surfaces.TerminalManager
	MacBrowser  *surfaces.MacOSWindowSurface
	MacClaude   *surfaces.MacOSWindowSurface
	VNC         *surfaces.VNCSurfaceManager
	VNCBridge   *surfaces.VNCBridge

	Invalidate func()

	mu               sync.Mutex
	activeNav        string
	sidebarWidth     float32
	sidebarCollapsed bool
	activeTermID     string
	showJobLog       bool
	maintModal       *macOSMaintenanceModal

	account      accountState
	onboarding   onboardingState
	workbench    workbenchState
	browser      browserViewState
	proxy        proxyState
	safety       safetyViewState
	claudeAcct   claudeAccountsState
	tailscale    tailscaleState
	remoteHosts  remoteHostsState
	browserMgmt  browserMgmtState
	settings     settingsViewState

	pendingToastMu sync.Mutex
	pendingToasts  []string
}

type macOSMaintenanceModal struct {
	purpose string
	surface *surfaces.MacOSLoginSurface
}

func NewRootView(
	store *core.Store,
	bridge core.HostBridge,
	audio *core.AudioStream,
	clip *core.ClipboardSync,
	termMgr *surfaces.TerminalManager,
	macBrowser *surfaces.MacOSWindowSurface,
	macClaude *surfaces.MacOSWindowSurface,
	vncMgr *surfaces.VNCSurfaceManager,
	vncBridge *surfaces.VNCBridge,
	invalidate func(),
) *RootView {
	client := store.Client()
	snap := store.Snapshot()
	w := snap.ClientPrefs.SidebarWidth
	if w < 184 || w > 360 {
		w = 218
	}
	rv := &RootView{
		Store:        store,
		Client:       client,
		Bridge:       bridge,
		Audio:        audio,
		Clipboard:    clip,
		Devices:      core.NewAccountDeviceService(client),
		Terminals:    termMgr,
		MacBrowser:   macBrowser,
		MacClaude:    macClaude,
		VNC:          vncMgr,
		VNCBridge:    vncBridge,
		Invalidate:   invalidate,
		activeNav:    NavTerminal,
		sidebarWidth: w,
	}
	rv.proxy.portStr = "7890"
	rv.proxy.intervalStr = "24"
	rv.remoteHosts.form.Port = 22
	rv.remoteHosts.portStr = "22"
	rv.remoteHosts.form.Auth.Kind = "agent"
	rv.remoteHosts.form.Platform = "linux"
	rv.settings.resourceCPUs = 4
	rv.settings.resourceMem = 8
	rv.settings.resourceDisk = 64

	store.SetOnToast(func(_ string, msg string) {
		rv.PushToast(msg)
	})
	return rv
}

func (r *RootView) PushToast(msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	r.pendingToastMu.Lock()
	r.pendingToasts = append(r.pendingToasts, msg)
	r.pendingToastMu.Unlock()
	if r.Invalidate != nil {
		r.Invalidate()
	}
}

func (r *RootView) SelectNav(section string) {
	r.mu.Lock()
	r.activeNav = section
	r.mu.Unlock()
	if r.Invalidate != nil {
		r.Invalidate()
	}
}

func (r *RootView) ActiveNav() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeNav
}

func (r *RootView) OpenDiagnosticsExport() {
	if r.Bridge == nil {
		return
	}
	go func() {
		dest, err := r.Bridge.SaveDialog(core.NativeDialogOptions{
			Title:       "导出诊断包",
			DefaultPath: "cage-diagnostics.zip",
			ButtonLabel: "导出",
		})
		if err != nil || dest == "" {
			return
		}
		res, err := r.Client.RunFileCommand(context.Background(), "diagnostics", dest)
		if err != nil {
			r.PushToast("导出诊断包失败：" + err.Error())
			return
		}
		_ = res
		r.PushToast("已导出诊断包至：" + dest)
		_ = r.Bridge.ShowItemInFolder(dest)
	}()
}

// View is the root MyGo ui.View entry point called on the UI thread for every frame.
func (r *RootView) View(c *ui.Context) {
	r.drainToasts(c)
	snap := r.Store.Snapshot()
	ApplyThemePreference(c, snap.ClientPrefs.ThemeMode)
	pal := PaletteFor(c)
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	} else {
		c.Root().Background(pal.Bg)
	}

	r.Terminals.SetStyle(snap.Appearance.CodeFontSize, pal.Dark)
	r.Terminals.Reconcile(snap.Terminals)

	// Keep guest media streams synchronized with environment readiness & preferences.
	r.Audio.SetMuted(snap.ClientPrefs.AudioMuted)
	r.Audio.SetVolume(snap.ClientPrefs.AudioVolume)
	streamActive := snap.Connected && !snap.Locked && snap.Account.Unlocked && snap.Status.Ready && snap.Status.VMRunning
	r.Audio.EnsureRunning(context.Background(), streamActive && !snap.ClientPrefs.AudioMuted)
	r.Clipboard.EnsureRunning(context.Background(), streamActive)

	if snap.Booting {
		r.renderBootLoading(c, pal)
		return
	}
	if snap.BootError != "" && !snap.Connected {
		r.renderBootError(c, pal, snap)
		return
	}
	if snap.StatusLoaded && snap.Status.StorageRecovery.Required {
		r.renderStorageRecovery(c, pal, snap)
		return
	}
	if snap.Connected && ((snap.Context.ClientOnly && snap.Context.Mode != "remote") || (snap.Context.DesktopGateway && snap.Context.Mode == "none")) {
		r.renderRemoteHostsView(c, pal, snap)
		return
	}
	if !snap.AccountLoaded {
		r.renderBootLoading(c, pal)
		return
	}
	r.account.mu.Lock()
	hasPendingRK := r.account.pendingRecoveryKey != ""
	r.account.mu.Unlock()
	if hasPendingRK || !snap.Account.Exists || snap.Locked || !snap.Account.Unlocked {
		r.renderAccountGate(c, pal, snap)
		return
	}
	if snap.Context.Mode != "remote" {
		if !snap.InitLoaded {
			r.renderBootLoading(c, pal)
			return
		}
		if snap.Account.InitialMacSetup || snap.Init.Required {
			if snap.Account.MacOSAccountHost && !snap.Account.MacOSCredentialsReady {
				r.renderMacOSCredentialsGate(c, pal, snap)
				return
			}
			r.renderOnboarding(c, pal, snap)
			return
		}
		if snap.Account.MacOSAccountHost && snap.Account.GuestSystem == "macos" && !snap.Account.MacOSCredentialsReady {
			r.renderMacOSCredentialsGate(c, pal, snap)
			return
		}
	}
	r.renderWorkbench(c, pal, snap)
}

func (r *RootView) drainToasts(c *ui.Context) {
	r.pendingToastMu.Lock()
	items := r.pendingToasts
	r.pendingToasts = nil
	r.pendingToastMu.Unlock()
	for _, msg := range items {
		c.Toast(msg)
	}
}

// ApplyThemePreference synchronizes MyGo ThemeSource without thrashing.
func ApplyThemePreference(c *ui.Context, mode string) {
	if c == nil {
		return
	}
	var want *ui.Theme
	switch mode {
	case "dark":
		want = ui.DarkTheme()
	case "light":
		want = ui.LightTheme()
	default:
		if c.Theme() != nil && c.Theme().Dark {
			want = ui.DarkTheme()
		} else {
			want = ui.LightTheme()
		}
	}
	if want != nil {
		if want.Dark {
			want = want.WithAccent(ui.Hex("#0a84ff"))
			want.Background = ui.Hex("#1e1e1e")
			want.Surface = ui.Hex("#3a3a40")
			want.SurfaceHover = ui.Hex("#46464d")
			want.SurfacePressed = ui.Hex("#52525a")
			want.Border = ui.RGBA(255, 255, 255, 0.12)
			want.Text = ui.Hex("#f5f5f7")
			want.TextMuted = ui.Hex("#98989d")
		} else {
			want = want.WithAccent(ui.Hex("#007aff"))
			want.Background = ui.Hex("#ffffff")
			want.Surface = ui.Hex("#e9e9ee")
			want.SurfaceHover = ui.Hex("#e0e0e7")
			want.SurfacePressed = ui.Hex("#d4d4dd")
			want.Border = ui.RGBA(0, 0, 0, 0.10)
			want.Text = ui.Hex("#1d1d1f")
			want.TextMuted = ui.Hex("#6e6e73")
		}
		want.Font = "-apple-system, SF Pro Text, PingFang SC, Helvetica Neue, sans-serif"
		want.Radius = 8
		c.SetTheme(want)
	}
}

// SyncNativeAppTheme mirrors client theme preference onto mygo.Theme (safe only when app is running).
func SyncNativeAppTheme(mode string) {
	switch mode {
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// Shared UI card & layout helpers used across all views to avoid repetition.

func Card(c *ui.Context, pal Palette, fn func()) ui.Element {
	col := ui.Column(c).
		Background(pal.CardBg).
		Border(1, pal.Border).
		Radius(10).
		Padding(16, 18).
		Gap(10)
	if !pal.Dark {
		col.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.04))
	}
	return col.Children(fn)
}

func InsetDivider(c *ui.Context, pal Palette) {
	ui.Box(c).FillWidth().Height(1).Background(pal.Border)
}

func SectionHeading(c *ui.Context, pal Palette, title, subtitle string) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, title).FontSize(15).Bold().TextColor(pal.Text)
		if subtitle != "" {
			ui.Text(c, subtitle).FontSize(12).TextColor(pal.Muted)
		}
	})
}

func PillBadge(c *ui.Context, label string, fg, bg ui.Color) ui.Element {
	return ui.Box(c).Background(bg).Radius(999).Padding(2, 9).Children(func() {
		ui.Text(c, label).FontSize(11).Bold().TextColor(fg)
	})
}

func ErrorBanner(c *ui.Context, pal Palette, msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	ui.Row(c).Background(pal.DangerSoft).Border(1, pal.Danger).Radius(8).Padding(9, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, msg).FontSize(12).TextColor(pal.Danger).Grow(1)
	})
}

func InfoBanner(c *ui.Context, pal Palette, msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	ui.Row(c).Background(pal.AccentSoft).Border(1, pal.Accent).Radius(8).Padding(9, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, msg).FontSize(12).TextColor(pal.Accent).Grow(1)
	})
}

func KeyValueRow(c *ui.Context, pal Palette, label, value string) {
	ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(16).Padding(4, 0).Children(func() {
		ui.Text(c, label).FontSize(13).TextColor(pal.Text)
		ui.Text(c, value).FontSize(13).TextColor(pal.Muted)
	})
}

// PageScroll centers a maxWidth content column inside a scrollable backdrop so wide
// desktop windows do not leave an awkward empty right half.
func PageScroll(c *ui.Context, pal Palette, maxWidth, pad float32, fn func()) {
	if maxWidth > 860 {
		maxWidth = 860
	}
	ui.Scroll(c).Fill().Background(pal.Bg).Padding(20, 28).Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Box(c).MaxWidth(maxWidth).FillWidth().Children(fn)
		})
	})
}

// TitleBarClearance adds a draggable top strip for full-window views without a sidebar
// so macOS traffic lights never overlap content.
func TitleBarClearance(c *ui.Context) {
	bar := c.TitleBar()
	if bar.Height > 0 {
		ui.Box(c).FillWidth().Height(bar.Bottom + 6).DragWindow()
	}
}
