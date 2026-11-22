package modal

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
)

// SSHAuthKind selects how the SSH tunnel authenticates to the bastion.
type SSHAuthKind int

const (
	SSHAuthAgent SSHAuthKind = iota
	SSHAuthPrivateKey
	SSHAuthPassword
)

// ConnectionDraft carries user input from the connection form to the runtime.
// Password, SSHPassphrase and SSHPassword are ephemeral; ClearSecrets wipes
// them once handed off or when the modal is dismissed.
type ConnectionDraft struct {
	Editing       bool
	Engine        domain.Engine
	Name          string
	FilePath      string // SQLite
	Host          string
	Port          string
	Database      string
	Username      string
	Password      string
	SSHEnabled    bool
	SSHJumpPath   string // ordered pre-configured ProxyJump hops
	SSHHost       string
	SSHPort       string
	SSHUser       string
	SSHAuth       SSHAuthKind
	SSHKeyPath    string
	SSHPassphrase string
	SSHPassword   string
	Env           domain.Environment
	ReadOnly      bool
}

// ClearSecrets drops ephemeral credential fields from the draft.
func (d *ConnectionDraft) ClearSecrets() {
	d.Password = ""
	d.SSHPassphrase = ""
	d.SSHPassword = ""
}

// ConnectionState owns the transient UI state of the connection modal.
type ConnectionState struct {
	Open        bool
	Section     int // 0=General, 1=SSH Tunnel, 2=Safety
	Draft       ConnectionDraft
	Error       string
	TestSuccess string
}

// OpenNew resets the modal for a fresh connection profile.
func (s *ConnectionState) OpenNew() {
	s.Open = true
	s.Section = 0
	s.Error = ""
	s.TestSuccess = ""
	s.Draft = ConnectionDraft{
		Engine:  domain.EnginePostgres,
		Host:    "127.0.0.1",
		Port:    "5432",
		SSHPort: "22",
		Env:     domain.EnvDev,
	}
}

// Close hides the modal and clears ephemeral secrets immediately.
func (s *ConnectionState) Close() {
	s.Open = false
	s.Error = ""
	s.TestSuccess = ""
	s.Draft.ClearSecrets()
}

// ConnectionCallbacks receives submit and connectivity test actions.
type ConnectionCallbacks struct {
	OnSave func(draft ConnectionDraft)
	OnTest func(draft ConnectionDraft)
}

var engineChoices = [...]domain.Engine{
	domain.EnginePostgres,
	domain.EngineMySQL,
	domain.EngineSQLite,
}

var envChoices = [...]domain.Environment{
	domain.EnvDev,
	domain.EnvStaging,
	domain.EnvProd,
}

// Connection renders the native desktop connection dialog when state.Open is true.
func Connection(c *ui.Context, k theme.Set, state *ConnectionState, cb ConnectionCallbacks) {
	if !state.Open {
		return
	}
	p := k.P
	m := msgs(c)
	title := m.ConnModalNewTitle()
	if state.Draft.Editing {
		title = m.ConnModalEditTitle()
	}

	frame(c, k, &state.Open, 520, func() {
		state.Close()
	}, func() {
		header(c, k, title, state.Draft.Name, state.Draft.Env, true, state.Close)
		sectionTabs(c, k, &state.Section, m.ConnTabGeneral(), m.ConnTabSSH(), m.ConnTabSafety())

		ui.Column(c).Padding(18, 20, 18, 16).Gap(10).MinHeight(228).Children(func() {
			edited := false
			switch state.Section {
			case 1:
				edited = sshSection(c, k, &state.Draft)
			case 2:
				safetySection(c, k, &state.Draft)
			default:
				edited = generalSection(c, k, &state.Draft)
			}
			if edited {
				state.Error = ""
				state.TestSuccess = ""
			}

			if state.Error != "" {
				ui.Row(c).Padding(6, 10).Gap(8).Radius(k.R.SM).
					Background(p.DeletedBg).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, theme.IconAlert).Size(13, 13).TextColor(p.Oxide)
					ui.Text(c, state.Error).FontSize(theme.SmallSize).TextColor(p.Ink).Grow(1).Shrink(1).MinWidth(0)
				})
			} else if state.TestSuccess != "" {
				ui.Row(c).Padding(6, 10).Gap(8).Radius(k.R.SM).
					Background(p.InsertedBg).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, theme.IconCheck).Size(13, 13).TextColor(p.Jade)
					ui.Text(c, state.TestSuccess).FontSize(theme.SmallSize).TextColor(p.Ink).Grow(1).Shrink(1).MinWidth(0)
				})
			}
		})

		seam(c, p.Seam)
		ui.Row(c).Height(46).PaddingX(16).Gap(8).AlignItems(ui.Center).Background(p.Matrix).Children(func() {
			if actionButton(c, k, "conn-test", m.ConnTest(), buttonGhost, nil) {
				if errMsg := validateDraft(c, &state.Draft); errMsg != "" {
					state.Error = errMsg
					state.TestSuccess = ""
				} else {
					state.Error = ""
					if cb.OnTest != nil {
						cb.OnTest(state.Draft)
					}
				}
			}
			ui.Spacer(c)
			if actionButton(c, k, "conn-cancel", m.ModalCancel(), buttonGhost, nil) {
				state.Close()
			}
			if actionButton(c, k, "conn-save", m.ConnSave(), buttonPrimary, nil) {
				if errMsg := validateDraft(c, &state.Draft); errMsg != "" {
					state.Error = errMsg
					state.TestSuccess = ""
				} else {
					draft := state.Draft
					state.Close()
					if cb.OnSave != nil {
						cb.OnSave(draft)
					}
				}
			}
		})
	})
}

func generalSection(c *ui.Context, k theme.Set, d *ConnectionDraft) bool {
	p := k.P
	m := msgs(c)
	changed := false

	engIdx := engineIndex(d.Engine)
	formRow(c, p, m.ConnFieldEngine(), func() {
		if segmented(c, k, "conn-engine", &engIdx, string(domain.EnginePostgres), string(domain.EngineMySQL), string(domain.EngineSQLite)) {
			prevDefault := defaultPort(d.Engine)
			d.Engine = engineChoices[engIdx]
			if d.Port == "" || d.Port == prevDefault {
				d.Port = defaultPort(d.Engine)
			}
			changed = true
		}
	})

	if textRow(c, k, "conn-name", m.ConnFieldName(), "orders-primary", &d.Name, false, false) {
		changed = true
	}

	if d.Engine == domain.EngineSQLite {
		if textRow(c, k, "conn-file", m.ConnFieldFile(), "~/dev/app.db", &d.FilePath, true, false) {
			changed = true
		}
		return changed
	}

	// Host + Port on one native desktop line.
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		lbl := ui.ButtonBase(c.Key("conn-host-lbl")).Width(labelColWidth).Justify(ui.End).AccessHidden(true).Children(func() {
			ui.Text(c, m.ConnFieldHost()).FontSize(theme.FontSize).TextColor(p.InkSoft).SingleLine()
		})
		hostIn := ui.TextInput(c.Key("conn-host"), &d.Host).
			Height(28).Padding(5, 8).Font(theme.MonoFamily).
			Background(p.Inset).Border(1, p.Seam).Radius(k.R.SM).
			Placeholder("127.0.0.1").Label(m.ConnFieldHost()).Grow(1).MinWidth(0)
		if lbl.Clicked() {
			hostIn.Focus()
		}
		if hostIn.Changed() {
			changed = true
		}
		ui.Text(c, ":").Font(theme.MonoFamily).TextColor(p.InkGhost)
		if ui.TextInput(c.Key("conn-port"), &d.Port).
			Width(72).Height(28).Padding(5, 8).Font(theme.MonoFamily).
			Background(p.Inset).Border(1, p.Seam).Radius(k.R.SM).
			Placeholder(defaultPort(d.Engine)).Label(m.ConnFieldPort()).Changed() {
			changed = true
		}
	})
	if textRow(c, k, "conn-db", m.ConnFieldDatabase(), "postgres", &d.Database, true, false) {
		changed = true
	}
	if textRow(c, k, "conn-user", m.ConnFieldUser(), "postgres", &d.Username, true, false) {
		changed = true
	}
	if textRow(c, k, "conn-password", m.ConnFieldPassword(), m.ConnPasswordPlaceholder(), &d.Password, false, true) {
		changed = true
	}
	return changed
}

func sshSection(c *ui.Context, k theme.Set, d *ConnectionDraft) bool {
	p := k.P
	m := msgs(c)
	changed := false

	ui.Row(c).Height(28).Padding(0, 0, 0, labelColWidth+12).Gap(10).AlignItems(ui.Center).Children(func() {
		lbl := ui.ButtonBase(c.Key("conn-ssh-label")).Grow(1).Justify(ui.Start).Children(func() {
			ui.Text(c, m.ConnSSHEnable()).FontWeight(500).TextColor(p.Ink)
		})
		if lbl.Clicked() {
			d.SSHEnabled = !d.SSHEnabled
			changed = true
		}
		if ui.Switch(c.Key("conn-ssh-enable"), &d.SSHEnabled).Label(m.ConnSSHEnable()).Changed() {
			changed = true
		}
	})
	if !d.SSHEnabled {
		return changed
	}

	if textRow(c, k, "conn-ssh-jump", m.ConnSSHJumpPath(), "bastion-1, prod-jump-2", &d.SSHJumpPath, true, false) {
		changed = true
	}
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		lbl := ui.ButtonBase(c.Key("conn-ssh-host-lbl")).Width(labelColWidth).Justify(ui.End).AccessHidden(true).Children(func() {
			ui.Text(c, m.ConnSSHHost()).FontSize(theme.FontSize).TextColor(p.InkSoft).SingleLine()
		})
		hostIn := ui.TextInput(c.Key("conn-ssh-host"), &d.SSHHost).
			Height(28).Padding(5, 8).Font(theme.MonoFamily).
			Background(p.Inset).Border(1, p.Seam).Radius(k.R.SM).
			Placeholder("bastion.internal").Label(m.ConnSSHHost()).Grow(1).MinWidth(0)
		if lbl.Clicked() {
			hostIn.Focus()
		}
		if hostIn.Changed() {
			changed = true
		}
		ui.Text(c, ":").Font(theme.MonoFamily).TextColor(p.InkGhost)
		if ui.TextInput(c.Key("conn-ssh-port"), &d.SSHPort).
			Width(72).Height(28).Padding(5, 8).Font(theme.MonoFamily).
			Background(p.Inset).Border(1, p.Seam).Radius(k.R.SM).
			Placeholder("22").Label(m.ConnFieldPort()).Changed() {
			changed = true
		}
	})
	if textRow(c, k, "conn-ssh-user", m.ConnSSHUser(), "ops", &d.SSHUser, true, false) {
		changed = true
	}

	authIdx := int(d.SSHAuth)
	formRow(c, p, m.ConnSSHAuth(), func() {
		if segmented(c, k, "conn-ssh-auth", &authIdx, m.ConnSSHAuthAgent(), m.ConnSSHAuthKey(), m.ConnSSHAuthPassword()) {
			d.SSHAuth = SSHAuthKind(authIdx)
			changed = true
		}
	})
	switch d.SSHAuth {
	case SSHAuthPrivateKey:
		if textRow(c, k, "conn-ssh-key", m.ConnSSHKeyPath(), "~/.ssh/id_ed25519", &d.SSHKeyPath, true, false) {
			changed = true
		}
		if textRow(c, k, "conn-ssh-passphrase", m.ConnSSHPassphrase(), m.ConnPasswordPlaceholder(), &d.SSHPassphrase, false, true) {
			changed = true
		}
	case SSHAuthPassword:
		if textRow(c, k, "conn-ssh-password", m.ConnFieldPassword(), m.ConnPasswordPlaceholder(), &d.SSHPassword, false, true) {
			changed = true
		}
	}
	return changed
}

func safetySection(c *ui.Context, k theme.Set, d *ConnectionDraft) {
	p := k.P
	m := msgs(c)

	// Clean horizontal Environment segmented picker—no three-card grid or
	// filler copy.
	formRow(c, p, m.ConnFieldEnv(), func() {
		envIdx := envIndex(d.Env)
		parts := ui.SegmentedBase(c.Key("conn-env"), &envIdx, len(envChoices))
		parts.Track.Height(28).Padding(2).Gap(2).AlignItems(ui.Stretch).
			Background(p.Inset).Radius(k.R.SM).Grow(1).Children(func() {
			for i, env := range envChoices {
				active := i == envIdx
				ec := p.Env(env)
				seg := parts.Segment(i).Grow(1).Center().Gap(6).PaddingX(10).Radius(k.R.XS)
				fg := p.InkFaint
				if active {
					seg.Background(p.Canvas).Border(1, p.Seam)
					fg = p.Ink
				} else if seg.Hovered() {
					fg = p.InkSoft
				}
				seg.Children(func() {
					ui.Box(c).Size(6, 6).Radius(3).Background(ec)
					ui.Text(c, m.EnvTag(string(env))).FontSize(11).FontWeight(600).TextColor(fg).SingleLine()
				})
			}
		})
		if parts.Track.Changed() {
			d.Env = envChoices[envIdx]
		}
	})

	ui.Box(c).Height(4)
	seam(c, p.Seam)
	ui.Box(c).Height(4)

	// Clean native left-text / right-switch setting row—no surrounding box.
	ui.Row(c).Padding(2, 0, 2, 12).Gap(16).AlignItems(ui.Center).Children(func() {
		lbl := ui.ButtonBase(c.Key("conn-readonly-label")).Column().AlignItems(ui.Start).Gap(2).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, m.ConnReadOnlyLabel()).FontWeight(500).TextColor(p.Ink)
			ui.Text(c, m.ConnReadOnlyHint()).FontSize(theme.SmallSize).TextColor(p.InkFaint)
		})
		if lbl.Clicked() {
			d.ReadOnly = !d.ReadOnly
		}
		ui.Switch(c.Key("conn-readonly"), &d.ReadOnly).Label(m.ConnReadOnlyLabel())
	})
}

func validateDraft(c *ui.Context, d *ConnectionDraft) string {
	m := msgs(c)
	if strings.TrimSpace(d.Name) == "" {
		return m.ConnValidationRequired(m.ConnFieldName())
	}
	if d.Engine == domain.EngineSQLite {
		if strings.TrimSpace(d.FilePath) == "" {
			return m.ConnValidationRequired(m.ConnFieldFile())
		}
		return ""
	}
	if strings.TrimSpace(d.Host) == "" {
		return m.ConnValidationRequired(m.ConnFieldHost())
	}
	if d.SSHEnabled && strings.TrimSpace(d.SSHHost) == "" && strings.TrimSpace(d.SSHJumpPath) == "" {
		return m.ConnValidationRequired(m.ConnSSHHost())
	}
	return ""
}

func engineIndex(e domain.Engine) int {
	for i, choice := range engineChoices {
		if choice == e {
			return i
		}
	}
	return 0
}

func envIndex(e domain.Environment) int {
	for i, choice := range envChoices {
		if choice == e {
			return i
		}
	}
	return 0
}

func defaultPort(e domain.Engine) string {
	switch e {
	case domain.EngineMySQL:
		return "3306"
	case domain.EngineSQLite:
		return ""
	default:
		return "5432"
	}
}
