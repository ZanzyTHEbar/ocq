package core

import (
	"encoding/json"
	"errors"
	"os"
)

// Only the bridge's original same-user validation context may cross into an
// explicitly isolated HC core. No caller-selected environment or executable.
var nativeClientContextKeys = []string{"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "OPENCODE_CLI_CONFIG_CONTENT", "OPENCODE_PASSWORD", "OPENCODE_SERVER_PASSWORD", "TMPDIR"}

func NativeClientLaunchContext() (string, error) {
	values := map[string]string{}
	for _, key := range nativeClientContextKeys {
		if value, present := os.LookupEnv(key); present {
			values[key] = value
		}
	}
	raw, err := json.Marshal(values)
	if err != nil || len(raw) > 256<<10 {
		return "", errors.New("native client context exceeds bound")
	}
	return string(raw), nil
}

func restoreNativeClientContext() error {
	raw, present := os.LookupEnv("OCQ_CLIENT_CONTEXT")
	if !present {
		return nil
	} // Existing direct CLI context is unchanged.
	if len(raw) > 256<<10 {
		return errors.New("native client context exceeds bound")
	}
	var values map[string]string
	if json.Unmarshal([]byte(raw), &values) != nil {
		return errors.New("native client context invalid")
	}
	allowed := map[string]bool{}
	for _, key := range nativeClientContextKeys {
		allowed[key] = true
	}
	for key := range values {
		if !allowed[key] {
			return errors.New("unsupported native client environment key")
		}
	}
	for _, key := range nativeClientContextKeys {
		if err := os.Unsetenv(key); err != nil {
			return err
		}
	}
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return os.Unsetenv("OCQ_CLIENT_CONTEXT")
}
