// Package launchd wraps launchctl and plist helpers for installing the agent as a LaunchAgent.
// It is used by cli agent up/down commands.

package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

type Spec struct {
	Label       string
	ProgramArgs []string
	RunAtLoad   bool
	KeepAlive   bool
	StdoutPath  string
	StderrPath  string
}

func Install(spec Spec) (string, error) {
	if spec.Label == "" {
		return "", fmt.Errorf("launchd label is required")
	}
	plistPath, err := plistPathForLabel(spec.Label)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return "", fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	content, err := renderPlist(spec)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(plistPath, content, 0o644); err != nil {
		return "", fmt.Errorf("write plist: %w", err)
	}
	return plistPath, nil
}

func Bootstrap(plistPath string) error {
	if plistPath == "" {
		return fmt.Errorf("plist path is required")
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	cmd := exec.Command("/bin/launchctl", "bootstrap", target, plistPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %v (%s)", err, string(output))
	}
	return nil
}

func Bootout(plistPath string) error {
	if plistPath == "" {
		return fmt.Errorf("plist path is required")
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	cmd := exec.Command("/bin/launchctl", "bootout", target, plistPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootout failed: %v (%s)", err, string(output))
	}
	return nil
}

func Print(label string) (string, error) {
	if label == "" {
		return "", fmt.Errorf("launchd label is required")
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	cmd := exec.Command("/bin/launchctl", "print", target)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("launchctl print failed: %v", err)
	}
	return string(output), nil
}

func Uninstall(label string) (string, error) {
	plistPath, err := plistPathForLabel(label)
	if err != nil {
		return "", err
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove plist: %w", err)
	}
	return plistPath, nil
}

func PlistPath(label string) (string, error) {
	return plistPathForLabel(label)
}

func plistPathForLabel(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func renderPlist(spec Spec) ([]byte, error) {
	if len(spec.ProgramArgs) == 0 {
		return nil, fmt.Errorf("program arguments are required")
	}
	data := struct {
		Label      string
		Args       []string
		RunAtLoad  bool
		KeepAlive  bool
		StdoutPath string
		StderrPath string
	}{
		Label:      spec.Label,
		Args:       spec.ProgramArgs,
		RunAtLoad:  spec.RunAtLoad,
		KeepAlive:  spec.KeepAlive,
		StdoutPath: spec.StdoutPath,
		StderrPath: spec.StderrPath,
	}

	funcs := template.FuncMap{
		"xml": func(value string) string {
			var out bytes.Buffer
			_ = xml.EscapeText(&out, []byte(value))
			return out.String()
		},
	}
	tpl := template.Must(template.New("plist").Funcs(funcs).Parse(plistTemplate))
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render plist: %w", err)
	}
	return buf.Bytes(), nil
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{xml .Label}}</string>
	<key>ProgramArguments</key>
	<array>
{{- range .Args }}
		<string>{{xml .}}</string>
{{- end }}
	</array>
	<key>RunAtLoad</key>
	<{{ if .RunAtLoad }}true{{ else }}false{{ end }}/>
	<key>KeepAlive</key>
	<{{ if .KeepAlive }}true{{ else }}false{{ end }}/>
{{- if .StdoutPath }}
	<key>StandardOutPath</key>
		<string>{{xml .StdoutPath}}</string>
{{- end }}
{{- if .StderrPath }}
	<key>StandardErrorPath</key>
		<string>{{xml .StderrPath}}</string>
{{- end }}
</dict>
</plist>
`
