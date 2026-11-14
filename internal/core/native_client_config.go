package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// JSONC lexing preserves strings exactly and supports comments/trailing commas.
// Invalid/unsupported documents fail explicitly, never erase valid plugins.
func cliJSONC(raw []byte) (map[string]any, error) {
	if len(raw) > WorkRequestBytes || !utf8.Valid(raw) {
		return nil, errors.New("CLI config exceeds safe merge bound")
	}
	clean := append([]byte(nil), raw...)
	inString, escape := false, false
	for i := 0; i < len(clean); i++ {
		c := clean[i]
		if inString {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == '/' && i+1 < len(clean) {
			if clean[i+1] == '/' {
				clean[i], clean[i+1] = ' ', ' '
				i += 2
				for i < len(clean) && clean[i] != '\n' {
					clean[i] = ' '
					i++
				}
				i--
				continue
			}
			if clean[i+1] == '*' {
				clean[i], clean[i+1] = ' ', ' '
				i += 2
				found := false
				for i+1 < len(clean) {
					if clean[i] == '*' && clean[i+1] == '/' {
						clean[i], clean[i+1] = ' ', ' '
						i++
						found = true
						break
					}
					if clean[i] != '\n' && clean[i] != '\r' {
						clean[i] = ' '
					}
					i++
				}
				if !found {
					return nil, errors.New("unterminated JSONC comment")
				}
			}
		}
	}
	inString, escape = false, false
	for i, c := range clean {
		if inString {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == ',' {
			next := i + 1
			for next < len(clean) && bytes.ContainsRune([]byte(" \r\n\t"), rune(clean[next])) {
				next++
			}
			if next < len(clean) && (clean[next] == ']' || clean[next] == '}') {
				previous := i - 1
				for previous >= 0 && bytes.ContainsRune([]byte(" \r\n\t"), rune(clean[previous])) {
					previous--
				}
				if previous < 0 || bytes.ContainsRune([]byte("[{,:"), rune(clean[previous])) {
					return nil, errors.New("invalid JSONC trailing comma")
				}
				clean[i] = ' '
			}
		}
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(clean))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, errors.New("CLI JSONC cannot be safely merged")
	}
	var extra any
	if out == nil || dec.Decode(&extra) != io.EOF {
		return nil, errors.New("CLI config must be one JSONC object")
	}
	return out, nil
}

func cliConfigFile(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, WorkRequestBytes+1))
	if err != nil {
		return nil, err
	}
	return cliJSONC(b)
}

// BuildNativeClientOverlay performs no config writes. Existing inline fields
// remain intact except the disclosed client-only prompt permission mode.
func BuildNativeClientOverlay(configDir, inline, asset string, options ClientOptions) (string, error) {
	base, err := cliConfigFile(filepath.Join(configDir, "cli.json"))
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(configDir, "cli.json")); os.IsNotExist(err) {
		legacy, e := cliConfigFile(filepath.Join(configDir, "tui.json"))
		if e != nil {
			return "", e
		}
		// Native CLI may migrate a legacy plugin array. Refuse rather than
		// silently overriding a valid migrated set we cannot reproduce here.
		if legacy["plugin"] != nil || legacy["plugin_enabled"] != nil {
			return "", errors.New("legacy TUI plugins require explicit native CLI migration before safe handoff; no config changed")
		}
	}
	overlay := map[string]any{}
	if inline != "" {
		overlay, err = cliJSONC([]byte(inline))
		if err != nil {
			return "", err
		}
	}
	var plugins []any
	value, present := base["plugins"]
	if selected, ok := overlay["plugins"]; ok {
		value, present = selected, true
	}
	if present {
		var ok bool
		plugins, ok = value.([]any)
		if !ok {
			return "", errors.New("effective CLI plugins must be an array; no silent replacement")
		}
	}
	kept := append([]any{}, plugins...)
	for _, p := range kept {
		switch entry := p.(type) {
		case string:
			if entry == asset {
				return "", errors.New("handoff plugin is already configured; refuse duplicate options")
			}
		case map[string]any:
			name, ok := entry["package"].(string)
			if !ok || name == "" {
				return "", errors.New("unsupported CLI plugin declaration; no plugin dropped")
			}
			if name == asset {
				return "", errors.New("handoff plugin is already configured; refuse duplicate options")
			}
		default:
			return "", errors.New("unsupported CLI plugin declaration; no plugin dropped")
		}
	}
	overlay["plugins"] = append(kept, map[string]any{"package": asset, "options": options})
	session := map[string]any{}
	for _, config := range []map[string]any{base, overlay} {
		if value, present := config["session"]; present {
			fields, ok := value.(map[string]any)
			if !ok {
				return "", errors.New("unsupported CLI session config; cannot enforce prompt-only permissions")
			}
			for key, value := range fields {
				session[key] = value
			}
		}
	}
	session["permissions"] = "prompt"
	overlay["session"] = session
	b, err := json.Marshal(overlay)
	if len(b) > WorkRequestBytes {
		return "", errors.New("native client overlay exceeds bound")
	}
	return string(b), err
}
