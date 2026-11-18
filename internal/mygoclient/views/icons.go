package views

import (
	"github.com/egoist/mygo/ui"
)

func svgIcon(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`))
}

var (
	// Brand & Window
	IconCageBrand     = svgIcon(`<rect x="4" y="4" width="16" height="16" rx="4"/><path d="M9 9h6v6H9z"/><path d="M9 4v16M15 4v16"/>`)
	IconSidebarToggle = svgIcon(`<rect x="3" y="4" width="18" height="16" rx="3"/><path d="M9 4v16"/>`)

	// Sidebar Navigation
	IconTerminal      = svgIcon(`<polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/>`)
	IconBrowser       = svgIcon(`<circle cx="12" cy="12" r="9"/><line x1="3" y1="12" x2="21" y2="12"/><path d="M12 3a14 14 0 0 1 0 18a14 14 0 0 1 0-18z"/>`)
	IconClaudeDesktop = svgIcon(`<rect x="3" y="4" width="18" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="18" x2="12" y2="21"/>`)
	IconProxy         = svgIcon(`<circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="12" r="2.5"/><circle cx="6" cy="18" r="2.5"/><path d="M8.3 7.2L15.7 10.8M15.7 13.2L8.3 16.8"/>`)
	IconShield        = svgIcon(`<path d="M12 3l7 3v5c0 4.5-3 8.2-7 10-4-1.8-7-5.5-7-10V6l7-3z"/><polyline points="9 12 11 14 15 10"/>`)
	IconTailscale     = svgIcon(`<circle cx="12" cy="5" r="2"/><circle cx="6" cy="12" r="2"/><circle cx="18" cy="12" r="2"/><circle cx="12" cy="19" r="2"/><line x1="10.6" y1="6.4" x2="7.4" y2="10.6"/><line x1="13.4" y1="6.4" x2="16.6" y2="10.6"/><line x1="7.4" y1="13.4" x2="10.6" y2="17.6"/><line x1="16.6" y1="13.4" x2="13.4" y2="17.6"/>`)
	IconCloudSSH      = svgIcon(`<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><circle cx="7" cy="7.5" r="0.8"/><circle cx="7" cy="16.5" r="0.8"/>`)
	IconAccounts      = svgIcon(`<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>`)
	IconVault         = svgIcon(`<rect x="3" y="11" width="18" height="10" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/><circle cx="12" cy="16" r="1"/>`)
	IconSettings      = svgIcon(`<line x1="4" y1="7" x2="20" y2="7"/><line x1="4" y1="17" x2="20" y2="17"/><circle cx="9" cy="7" r="2"/><circle cx="15" cy="17" r="2"/>`)

	// Actions & Controls
	IconFolder      = svgIcon(`<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z"/>`)
	IconPlus        = svgIcon(`<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>`)
	IconClose       = svgIcon(`<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>`)
	IconArrowLeft   = svgIcon(`<polyline points="15 18 9 12 15 6"/>`)
	IconArrowRight  = svgIcon(`<polyline points="9 18 15 12 9 6"/>`)
	IconRefresh     = svgIcon(`<polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>`)
	IconLock        = svgIcon(`<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>`)
	IconIncognito   = svgIcon(`<path d="M3 11h18"/><path d="M6 11l2-6h8l2 6"/><circle cx="8.5" cy="16" r="2.5"/><circle cx="15.5" cy="16" r="2.5"/><path d="M11 16h2"/>`)
	IconUpload      = svgIcon(`<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/>`)
	IconDownload    = svgIcon(`<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/>`)
	IconPlay        = svgIcon(`<polygon points="6 4 20 12 6 20 6 4"/>`)
	IconStop        = svgIcon(`<rect x="6" y="6" width="12" height="12" rx="2"/>`)
	IconVolume      = svgIcon(`<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M19 5a10 10 0 0 1 0 14"/>`)
	IconVolumeMute  = svgIcon(`<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="22" y1="9" x2="16" y2="15"/><line x1="16" y1="9" x2="22" y2="15"/>`)
	IconChevronDown = svgIcon(`<polyline points="6 9 12 15 18 9"/>`)
)

func StatusDot(c *ui.Context, color ui.Color, size float32) ui.Element {
	if size <= 0 {
		size = 7
	}
	return ui.Box(c).Size(size, size).Radius(size / 2).Background(color)
}

func IconButton(c *ui.Context, pal Palette, key string, icon *ui.SVG, label string, disabled bool) bool {
	btn := ui.ButtonBase(c.Key(key)).
		Size(28, 28).
		Center().
		Radius(6).
		Disabled(disabled).
		Label(label).
		Tooltip(label)
	if !disabled && btn.Hovered() {
		btn.Background(pal.HoverBg)
	}
	fg := pal.Muted
	if !disabled && btn.Hovered() {
		fg = pal.Text
	}
	btn.Children(func() {
		ui.Icon(c, icon).FontSize(14).TextColor(fg)
	})
	return btn.Clicked()
}

func CompactChipButton(c *ui.Context, pal Palette, key string, icon *ui.SVG, text string, primary bool, disabled bool) bool {
	btn := ui.ButtonBase(c.Key(key)).
		Height(26).
		Padding(0, 9).
		Radius(6).
		Gap(5).
		AlignItems(ui.Center).
		Disabled(disabled).
		Label(text)
	fg := pal.Text
	if primary {
		fg = ui.Hex("#ffffff")
		btn.Background(pal.Accent)
	} else {
		btn.Background(pal.CardBg).Border(1, pal.Border)
		if !disabled && btn.Hovered() {
			btn.Background(pal.ElevatedBg)
		}
	}
	btn.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).FontSize(12).TextColor(fg)
		}
		if text != "" {
			ui.Text(c, text).FontSize(12).TextColor(fg).SingleLine()
		}
	})
	return btn.Clicked()
}

// NativeTabChip renders a Safari/Magpie-style compact tab with optional inline close button.
func NativeTabChip(c *ui.Context, pal Palette, key string, icon *ui.SVG, title string, active bool, onClose func()) bool {
	row := ui.Row(c.Key("tab_" + key)).
		Height(28).
		Padding(0, 8).
		Radius(7).
		Gap(6).
		AlignItems(ui.Center)
	if active {
		row.Background(pal.CardBg).
			Border(1, pal.Border).
			Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.05))
	} else if row.Hovered() {
		row.Background(pal.HoverBg)
	}

	activated := false
	row.Children(func() {
		trigger := ui.ButtonBase(c.Key("tab_btn_" + key)).
			Gap(5).
			AlignItems(ui.Center).
			Label(title)
		fg := pal.Muted
		if active {
			fg = pal.Text
		} else if trigger.Hovered() {
			fg = pal.Text
		}
		trigger.Children(func() {
			if icon != nil {
				ui.Icon(c, icon).FontSize(12).TextColor(fg)
			}
			lbl := ui.Text(c, title).FontSize(12).TextColor(fg).SingleLine()
			if active {
				lbl.Bold()
			}
		})
		if trigger.Clicked() {
			activated = true
		}
		if onClose != nil {
			closeBtn := ui.ButtonBase(c.Key("tab_close_" + key)).
				Size(16, 16).
				Center().
				Radius(4).
				Label("关闭")
			if closeBtn.Hovered() {
				closeBtn.Background(pal.HoverBg)
			}
			closeBtn.Children(func() {
				ui.Icon(c, IconClose).FontSize(10).TextColor(pal.Muted)
			})
			if closeBtn.Clicked() {
				onClose()
			}
		}
	})
	return activated
}
