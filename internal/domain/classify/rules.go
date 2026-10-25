package classify

import "github.com/kshitijdhara/burnout-tracker/internal/domain/model"

// bundledApps maps macOS bundle ids and Windows/Linux executable names to
// categories. Matching is case-insensitive.
var bundledApps = map[model.Category][]string{
	model.CategoryTerminal: {
		"com.apple.Terminal", "com.googlecode.iterm2", "net.kovidgoyal.kitty", "com.github.wez.wezterm",
		"io.alacritty", "com.mitchellh.ghostty", "dev.warp.Warp-Stable",
		"WindowsTerminal.exe", "cmd.exe", "powershell.exe", "pwsh.exe", "wezterm-gui.exe", "alacritty.exe",
		"gnome-terminal-server", "konsole", "kitty", "alacritty", "wezterm-gui", "ghostty", "foot", "xterm",
	},
	model.CategoryCode: {
		"com.microsoft.VSCode", "com.microsoft.VSCodeInsiders", "com.todesktop.230313mzl4w4u92", "dev.zed.Zed",
		"com.sublimetext.4", "com.apple.dt.Xcode", "com.vscodium",
		"Code.exe", "Cursor.exe", "Zed.exe", "sublime_text.exe", "devenv.exe", "idea64.exe", "goland64.exe",
		"pycharm64.exe", "webstorm64.exe", "rider64.exe", "clion64.exe",
		"code", "cursor", "zed", "zeditor", "sublime_text",
	},
	model.CategoryBrowser: {
		"com.google.Chrome", "org.mozilla.firefox", "com.apple.Safari", "com.microsoft.edgemac",
		"company.thebrowser.Browser", "com.brave.Browser", "com.vivaldi.Vivaldi", "com.operasoftware.Opera",
		"chrome.exe", "firefox.exe", "msedge.exe", "brave.exe", "arc.exe", "vivaldi.exe", "opera.exe",
		"chrome", "chromium", "firefox", "brave", "vivaldi-bin",
	},
	model.CategoryComms: {
		"com.tinyspeck.slackmacgap", "com.microsoft.teams2", "com.apple.mail", "com.microsoft.Outlook",
		"com.hnc.Discord", "ru.keepcoder.Telegram", "net.whatsapp.WhatsApp", "com.apple.MobileSMS",
		"slack.exe", "ms-teams.exe", "Teams.exe", "OUTLOOK.EXE", "olk.exe", "Discord.exe", "Telegram.exe",
		"WhatsApp.exe", "thunderbird.exe", "slack", "discord", "telegram-desktop", "thunderbird", "evolution",
	},
	model.CategoryMeeting: {
		"us.zoom.xos", "com.cisco.webexmeetingsapp", "Zoom.exe", "CiscoCollabHost.exe", "zoom",
	},
	model.CategoryDocs: {
		"notion.id", "md.obsidian", "com.apple.iWork.Pages", "com.apple.iWork.Numbers", "com.apple.iWork.Keynote",
		"com.microsoft.Word", "com.microsoft.Excel", "com.microsoft.Powerpoint", "com.apple.Preview",
		"com.adobe.Reader", "com.apple.Notes", "com.figma.Desktop",
		"Notion.exe", "Obsidian.exe", "WINWORD.EXE", "EXCEL.EXE", "POWERPNT.EXE", "AcroRd32.exe", "Acrobat.exe",
		"Figma.exe", "obsidian", "notion-app", "libreoffice", "soffice.bin", "evince", "okular", "zathura",
	},
	model.CategoryMedia: {
		"com.spotify.client", "com.apple.Music", "com.apple.TV", "org.videolan.vlc", "com.apple.QuickTimePlayerX",
		"Spotify.exe", "vlc.exe", "spotify", "vlc", "mpv",
	},
	model.CategoryRemoteDesktop: {
		"com.microsoft.rdc.macos", "com.citrix.receiver.icaviewer.mac", "com.parallels.desktop.console",
		"com.vmware.fusion", "org.virtualbox.app.VirtualBoxVM",
		"mstsc.exe", "msrdc.exe", "CDViewer.exe", "vmware.exe", "VirtualBoxVM.exe", "remmina", "virt-viewer",
	},
}

var bundledPrefixes = []struct {
	prefix string
	cat    model.Category
}{
	{"com.jetbrains.", model.CategoryCode},
	{"com.google.android.studio", model.CategoryCode},
}

// sensitiveApps are recorded as private by default (privacy & security
// §5.1): time is kept, identity is dropped. Matching ignores case. Most
// banking, health and dating happens in a browser, where only the title
// says what it is; those sites are covered once title classification
// exists. Users add their own with privacy.exclude_apps.
var sensitiveApps = []string{
	// Password managers and keychains.
	"com.1password.1password", "com.agilebits.onepassword7", "com.bitwarden.desktop", "org.keepassxc.keepassxc",
	"com.apple.keychainaccess", "com.apple.Passwords", "com.dashlane.dashlanephonefinal",
	"com.lastpass.LastPass", "in.sinew.Enpass-Desktop", "me.proton.pass.electron", "com.keepersecurity.passwordmanager",
	"1Password.exe", "Bitwarden.exe", "KeePassXC.exe", "KeePass.exe", "Dashlane.exe", "Enpass.exe",
	"Proton Pass.exe", "NordPass.exe", "keeperpasswordmanager.exe",
	"1password", "bitwarden", "keepassxc", "seahorse", "enpass",
	// Banking, budgeting and tax.
	"com.moneymoney-app.retail", "com.iggsoftware.banktivity7", "com.quicken.Quicken", "com.moneydance.Moneydance",
	"org.gnucash.Gnucash", "qw.exe", "Moneydance.exe", "gnucash.exe", "gnucash", "kmymoney", "homebank",
	// Crypto wallets.
	"com.ledger.live", "io.trezor.TrezorSuite", "com.electron.exodus",
	"Ledger Live.exe", "Trezor Suite.exe", "Exodus.exe", "ledger-live-desktop", "exodus",
	// Health and therapy.
	"com.apple.Health", "com.headspace.Headspace", "com.calm.Calm",
}

var commandCategories = map[string]CommandCategory{
	"git": CmdVCS, "gh": CmdVCS, "hg": CmdVCS, "jj": CmdVCS, "svn": CmdVCS,
	"make": CmdBuild, "go": CmdBuild, "cargo": CmdBuild, "npm": CmdBuild, "pnpm": CmdBuild, "yarn": CmdBuild,
	"bun": CmdBuild, "node": CmdBuild, "python": CmdBuild, "python3": CmdBuild, "pytest": CmdBuild,
	"gradle": CmdBuild, "mvn": CmdBuild, "bazel": CmdBuild, "cmake": CmdBuild, "ninja": CmdBuild, "dotnet": CmdBuild,
	"kubectl": CmdInfra, "helm": CmdInfra, "terraform": CmdInfra, "tofu": CmdInfra, "docker": CmdInfra,
	"podman": CmdInfra, "aws": CmdInfra, "gcloud": CmdInfra, "az": CmdInfra, "k9s": CmdInfra, "ansible": CmdInfra,
	"ssh": CmdRemote, "scp": CmdRemote, "mosh": CmdRemote, "rsync": CmdRemote, "sftp": CmdRemote,
	"vim": CmdEditor, "nvim": CmdEditor, "vi": CmdEditor, "emacs": CmdEditor, "nano": CmdEditor, "hx": CmdEditor,
	"claude": CmdAgent, "codex": CmdAgent, "aider": CmdAgent, "gemini": CmdAgent,
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// subcommands lists the subcommands kept for allowlisted tools. Anything
// else the hook sends is dropped.
var subcommands = map[string]map[string]bool{
	"cargo":     set("add", "bench", "build", "check", "clean", "clippy", "doc", "fmt", "install", "new", "publish", "run", "test", "update"),
	"docker":    set("build", "buildx", "compose", "container", "cp", "exec", "image", "images", "inspect", "kill", "login", "logs", "network", "ps", "pull", "push", "restart", "rm", "rmi", "run", "start", "stop", "system", "tag", "volume"),
	"gh":        set("api", "auth", "browse", "issue", "pr", "release", "repo", "run", "search", "workflow"),
	"git":       set("add", "am", "bisect", "blame", "branch", "checkout", "cherry-pick", "clean", "clone", "commit", "diff", "fetch", "grep", "init", "log", "merge", "mv", "pull", "push", "rebase", "reflog", "remote", "reset", "restore", "revert", "rm", "show", "stash", "status", "switch", "tag", "worktree"),
	"go":        set("build", "clean", "doc", "env", "fmt", "generate", "get", "install", "list", "mod", "run", "test", "tool", "vet", "work"),
	"helm":      set("dependency", "get", "history", "install", "lint", "list", "package", "repo", "rollback", "search", "show", "status", "template", "test", "uninstall", "upgrade"),
	"kubectl":   set("annotate", "apply", "attach", "auth", "config", "cordon", "cp", "create", "debug", "delete", "describe", "diff", "drain", "edit", "exec", "explain", "expose", "get", "label", "logs", "patch", "port-forward", "rollout", "run", "scale", "set", "top", "uncordon", "wait"),
	"npm":       set("add", "audit", "build", "ci", "exec", "i", "init", "install", "link", "ls", "outdated", "pack", "publish", "remove", "run", "start", "test", "uninstall", "update", "version", "x"),
	"pnpm":      set("add", "audit", "build", "dev", "dlx", "exec", "i", "install", "publish", "remove", "run", "start", "test", "update"),
	"terraform": set("apply", "console", "destroy", "fmt", "import", "init", "output", "plan", "providers", "refresh", "show", "state", "validate", "workspace"),
	"yarn":      set("add", "audit", "build", "dev", "dlx", "install", "publish", "remove", "run", "start", "test", "upgrade"),
}
