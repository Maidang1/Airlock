//go:build darwin

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/macnative"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
	"github.com/sylvinus/agent-vm/internal/mygoclient/surfaces"
	"github.com/sylvinus/agent-vm/internal/mygoclient/views"
)

type mygoHostBridge struct {
	mu      sync.RWMutex
	win     *mygo.Window
	updater *macnative.AppUpdater
}

func (b *mygoHostBridge) setWindow(w *mygo.Window) {
	b.mu.Lock()
	b.win = w
	b.mu.Unlock()
}

func (b *mygoHostBridge) window() *mygo.Window {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.win
}

func (b *mygoHostBridge) OpenDialog(opts core.NativeDialogOptions) ([]string, error) {
	return mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      b.window(),
		Title:       opts.Title,
		Message:     opts.Message,
		DefaultPath: opts.DefaultPath,
		ButtonLabel: opts.ButtonLabel,
		Directory:   opts.Directory,
		Multiple:    opts.Multiple,
	})
}

func (b *mygoHostBridge) SaveDialog(opts core.NativeDialogOptions) (string, error) {
	return mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:            b.window(),
		Title:             opts.Title,
		Message:           opts.Message,
		DefaultPath:       opts.DefaultPath,
		ButtonLabel:       opts.ButtonLabel,
		CreateDirectories: true,
	})
}

func (b *mygoHostBridge) OpenExternal(rawURL string) error {
	return mygo.Shell.OpenExternal(rawURL)
}

func (b *mygoHostBridge) OpenPath(path string) error {
	return mygo.Shell.OpenPath(path)
}

func (b *mygoHostBridge) ShowItemInFolder(path string) error {
	mygo.Shell.ShowItemInFolder(path)
	return nil
}

func (b *mygoHostBridge) ShowAndFocusWindow() {
	if w := b.window(); w != nil {
		w.Show()
		w.Focus()
	}
}

func (b *mygoHostBridge) MarkWorkbenchReady() {
	if b.updater != nil {
		b.updater.MarkLaunchReady()
	}
}

func (b *mygoHostBridge) HandleUpdate(operation string) map[string]any {
	if b.updater == nil {
		return map[string]any{"phase": "manual"}
	}
	return b.updater.Handle(operation)
}

func maybeHandleTrialPasswordCLI(args []string) (handled bool, exitCode int) {
	for _, arg := range args {
		if arg != "--store-trial-guest-password" {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return true, 1
		}
		pw := strings.TrimRight(string(raw), "\r\n")
		if err := macnative.StoreTrialGuestPassword(pw); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return true, 1
		}
		return true, 0
	}
	return false, 0
}

func main() {
	profile := macnative.PinEnvironment()
	if handled, code := maybeHandleTrialPasswordCLI(os.Args[1:]); handled {
		os.Exit(code)
	}

	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	client := core.NewControllerClient(core.ClientOptions{
		ConfigPath: profile.ConfigPath,
		ClientOnly: profile.ClientOnly,
	})
	store := core.NewStore(client)
	audioStream := core.NewAudioStream(client)
	clipSync := core.NewClipboardSync(client)

	bridge := &mygoHostBridge{}
	var winMu sync.RWMutex
	var mainWin *mygo.Window

	invalidate := func() {
		winMu.RLock()
		w := mainWin
		winMu.RUnlock()
		if w != nil && !w.IsDestroyed() {
			w.Invalidate()
		}
	}

	var rootView *views.RootView
	termMgr := surfaces.NewTerminalManager(client, func(string) {
		if rootView != nil {
			rootView.SelectNav(views.NavBrowser)
		}
		store.TriggerRefresh()
	}, invalidate)
	macBrowser := surfaces.NewMacOSWindowSurface(client, true, invalidate)
	macClaude := surfaces.NewMacOSWindowSurface(client, false, invalidate)
	vncBridge := surfaces.NewVNCBridge(client, clipSync, invalidate)
	vncMgr := surfaces.NewVNCSurfaceManager(client, invalidate)

	var (
		allowQuit    atomic.Bool
		quitPromptMu sync.Mutex
		quitPrompted bool
	)

	updater := macnative.NewAppUpdater(
		profile,
		func(state map[string]any) {
			store.SetUpdateState(state)
		},
		func() {
			allowQuit.Store(true)
			mygo.RunOnMain(func() {
				mygo.App.Quit()
			})
		},
		func() {
			go func() {
				_ = client.Bootstrap(appCtx)
				store.TriggerRefresh()
			}()
		},
	)
	bridge.updater = updater
	store.SetUpdateState(updater.Snapshot())

	mygo.BindAs("VNCBridge", vncBridge)

	agentLoop := core.NewAgentNativeLoop(client, store, clipSync, bridge)
	rootView = views.NewRootView(
		store,
		bridge,
		audioStream,
		clipSync,
		termMgr,
		macBrowser,
		macClaude,
		vncMgr,
		vncBridge,
		invalidate,
	)

	views.SyncNativeAppTheme(store.Snapshot().ClientPrefs.ThemeMode)
	store.SetInvalidate(invalidate)
	store.SetOnUnlocked(func() {
		updater.MarkLaunchReady()
	})
	store.SetOnPrefsChanged(func(p core.ClientPreferences) {
		views.SyncNativeAppTheme(p.ThemeMode)
	})

	mygo.App.OnSecondInstance(func(_ []string, _ string) {
		mygo.RunOnMain(func() {
			bridge.ShowAndFocusWindow()
		})
	})

	mygo.App.OnActivate(func(_ bool) {
		bridge.ShowAndFocusWindow()
	})

	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if allowQuit.Load() || e.SessionEnding {
			return
		}
		if updater.PrepareForTermination(func() {
			allowQuit.Store(true)
			mygo.RunOnMain(func() {
				mygo.App.Quit()
			})
		}) {
			e.PreventDefault()
			return
		}

		snap := store.Snapshot()
		if snap.Connected && snap.Context.Mode != "remote" && snap.Status.VMRunning {
			e.PreventDefault()
			quitPromptMu.Lock()
			if quitPrompted {
				quitPromptMu.Unlock()
				return
			}
			quitPrompted = true
			quitPromptMu.Unlock()

			go func() {
				defer func() {
					quitPromptMu.Lock()
					quitPrompted = false
					quitPromptMu.Unlock()
				}()
				res, err := mygo.Dialog.Message(mygo.MessageOptions{
					Parent:             bridge.window(),
					Type:               mygo.MessageQuestion,
					Title:              "隔离环境仍在运行",
					Message:            "是否停止正在运行的虚拟机环境并退出 Airlock？",
					Detail:             "选择「保持后台运行」可让环境与定时任务在后台继续。",
					Buttons:            []string{"停止环境并退出", "保持后台运行", "取消"},
					DefaultButton:      0,
					CancelButton:       2,
					DestructiveButtons: []int{0},
				})
				if err != nil || res.Button == 2 || res.Button < 0 {
					return
				}
				if res.Button == 0 {
					_ = client.PostJSON(context.Background(), "/api/stop", map[string]any{}, nil)
				}
				allowQuit.Store(true)
				mygo.RunOnMain(func() {
					mygo.App.Quit()
				})
			}()
		}
	})

	mygo.App.OnQuit(func() {
		appCancel()
		agentLoop.Stop()
		macBrowser.Stop()
		macClaude.Stop()
		audioStream.Stop()
		clipSync.Stop()
		termMgr.CloseAll()
		vncMgr.CloseAll()
		store.Stop()
		updater.CancelForTermination()
	})

	mygo.App.WhenReady(func() {
		title := "Airlock"
		if profile.ClientOnly {
			title = "Airlock Client"
		}
		w := mygo.NewWindow(mygo.WindowOptions{
			Title:                title,
			Width:                1380,
			Height:               880,
			MinWidth:             1024,
			MinHeight:            660,
			TitleBarStyle:        mygo.TitleBarHidden,
			TrafficLightPosition: &mygo.Point{X: 16, Y: 14},
			Transparent:          true,
			Vibrancy:             mygo.VibrancySidebar,
			StateKey:             "main",
			Content:              ui.View(rootView.View),
		})
		winMu.Lock()
		mainWin = w
		winMu.Unlock()
		bridge.setWindow(w)
		vncMgr.SetWindow(w)

		w.OnClose(func(e *mygo.CloseEvent) {
			if allowQuit.Load() {
				return
			}
			// Keep macOS app alive in Dock when user closes the window.
			e.PreventDefault()
			w.Hide()
		})

		installMenus(rootView, store, bridge, w)

		store.Start(appCtx)
		agentLoop.Start(appCtx)
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
