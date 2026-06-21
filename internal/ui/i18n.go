package ui

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"fyne.io/fyne/v2"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/wireproxy"
)

//go:embed translations/*.json
var appTranslations embed.FS

type translateFunc func(string, ...any) string

var (
	translate      translateFunc = translateEnglishCatalog
	englishCatalog map[string]string
)

func init() {
	data, err := appTranslations.ReadFile("translations/en.json")
	if err != nil {
		fyne.LogError("Error loading app translations", err)
		return
	}
	err = json.Unmarshal(data, &englishCatalog)
	if err != nil {
		fyne.LogError("Error parsing app translations", err)
	}
}

func tr(message string, data ...any) string {
	return translate(message, data...)
}

func translateEnglishCatalog(message string, data ...any) string {
	text := message
	if translated, ok := englishCatalog[message]; ok {
		text = translated
	}
	if len(data) == 0 {
		return text
	}
	rendered, err := renderTranslation(message, text, data[0])
	if err != nil {
		fyne.LogError("Error rendering app translation", err)
		return text
	}
	return rendered
}

func renderTranslation(name, text string, data any) (string, error) {
	tmpl, err := template.New(name).Parse(text)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	err = tmpl.Execute(&out, data)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

func localizedErrorText(err error) string {
	if err == nil {
		return ""
	}
	return localizedText(err.Error())
}

func localizedText(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = localizedTextLine(line)
	}
	return strings.Join(lines, "\n")
}

func localizedTextLine(text string) string {
	if localized, ok := localizedStructuredText(text); ok {
		return localized
	}
	for _, item := range localizableErrors {
		if strings.Contains(text, item.err.Error()) {
			text = strings.ReplaceAll(text, item.err.Error(), tr(item.err.Error()))
		}
	}
	return tr(text)
}

func localizedStructuredText(text string) (string, bool) {
	if address, ok := strings.CutPrefix(text, "connected on "); ok {
		return tr("connected on {{.Address}}", map[string]any{
			"Address": address,
		}), true
	}
	if rest, ok := strings.CutPrefix(text, "listen SOCKS5 on "); ok {
		address, message, ok := strings.Cut(rest, ": ")
		if ok {
			return tr("listen SOCKS5 on {{.Address}}: {{.Message}}", map[string]any{
				"Address": address,
				"Message": localizedTextLine(message),
			}), true
		}
	}
	if message, ok := strings.CutPrefix(text, "start embedded WireGuard engine: "); ok {
		return tr("start embedded WireGuard engine: {{.Message}}", map[string]any{
			"Message": localizedTextLine(message),
		}), true
	}
	if message, ok := strings.CutPrefix(text, "load profiles: "); ok {
		return tr("load profiles: {{.Message}}", map[string]any{
			"Message": localizedTextLine(message),
		}), true
	}
	if message, ok := strings.CutPrefix(text, "save imported profiles: "); ok {
		return tr("save imported profiles: {{.Message}}", map[string]any{
			"Message": localizedTextLine(message),
		}), true
	}
	if message, ok := strings.CutPrefix(text, "import WireGuard config: "); ok {
		return tr("import WireGuard config: {{.Message}}", map[string]any{
			"Message": localizedTextLine(message),
		}), true
	}
	if message, ok := strings.CutPrefix(text, wireproxy.ErrConfigInvalid.Error()+": "); ok {
		return tr("wireproxy config validation failed: {{.Message}}", map[string]any{
			"Message": localizedTextLine(message),
		}), true
	}

	var address, first, second string
	n, _ := fmt.Sscanf(
		text,
		profile.ErrDuplicateBindAddress.Error()+": %s is used by %q and %q",
		&address,
		&first,
		&second,
	)
	if n == 3 {
		return tr("duplicate SOCKS5 bind address: {{.Address}} is used by \"{{.First}}\" and \"{{.Second}}\"", map[string]any{
			"Address": address,
			"First":   first,
			"Second":  second,
		}), true
	}

	var activeName string
	n, _ = fmt.Sscanf(
		text,
		profile.ErrDuplicateBindAddress.Error()+": %s is already used by active profile %q",
		&address,
		&activeName,
	)
	if n == 2 {
		return tr("duplicate SOCKS5 bind address: {{.Address}} is already used by active profile \"{{.Name}}\"", map[string]any{
			"Address": address,
			"Name":    activeName,
		}), true
	}

	return "", false
}

var localizableErrors = []struct {
	err error
}{
	{err: errRuntimeProfileEdit},
	{err: profile.ErrProfileNameRequired},
	{err: profile.ErrSocksHostRequired},
	{err: profile.ErrSocksPortNotNumber},
	{err: profile.ErrSocksPortOutOfRange},
	{err: profile.ErrWireGuardConfigMissing},
	{err: profile.ErrWireGuardConfigEmpty},
	{err: profile.ErrImportFileEmpty},
	{err: profile.ErrImportJSONInvalid},
	{err: profile.ErrImportProfilesEmpty},
	{err: profile.ErrDuplicateBindAddress},
	{err: wireproxy.ErrAlreadyConnected},
	{err: wireproxy.ErrConfigInvalid},
	{err: wireproxy.ErrSocks5Missing},
	{err: errNativeFileDialogUnavailable},
}
