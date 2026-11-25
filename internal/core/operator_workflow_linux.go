package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// OperatorRepository only inspects the explicitly selected checkout. It never
// chooses a repository, switches a branch, or initializes queue state.
func OperatorRepository(repo string) (string, error) {
	branch, err := mainGitRead(repo, "symbolic-ref", "HEAD")
	if err != nil {
		return "", err
	}
	_, _, _, err = inspectCanonicalMain(repo, branch)
	return branch, err
}

// VerifyOperatorNative checks packaging without starting an inspection client.
func VerifyOperatorNative() error {
	images, err := nativeReleaseImages()
	if err != nil {
		return err
	}
	config, err := nativeConfigDir()
	if err != nil {
		return err
	}
	if _, err = BuildNativeClientOverlay(config, os.Getenv("OPENCODE_CLI_CONFIG_CONTENT"), images.Asset, ClientOptions{}); err != nil {
		return err
	}
	_, err = exec.LookPath("workmux")
	return err
}

// VerifyOperatorProfiles checks owner-provisioned bytes against the real catalog.
// Catalog bytes (which may contain provider configuration) never leave the core.
func VerifyOperatorProfiles(repo, main, implementation, review string) error {
	return VerifyOperatorSelections(repo, []OperatorProfileSelection{{"main", main}, {"implementation", implementation}, {"review", review}})
}

type OperatorProfileSelection struct{ Role, Profile string }

func VerifyOperatorSelections(repo string, selections []OperatorProfileSelection) error {
	if len(selections) < 3 || len(selections) > 32 {
		return errors.New("bounded owner selections required")
	}
	return verifyOperatorSelections(repo, selections)
}

// VerifyOperatorImplementation checks the only execution profile used by a
// standalone task. It cannot validate or authorize a review profile.
func VerifyOperatorImplementation(repo, profile string) error {
	return verifyOperatorSelections(repo, []OperatorProfileSelection{{"implementation", profile}})
}

func verifyOperatorSelections(repo string, selections []OperatorProfileSelection) error {
	profiles := []MainProfile{}
	for _, selection := range selections {
		if selection.Role == "main" {
			p, err := parseMainProfile(selection.Profile)
			if err != nil {
				return err
			}
			profiles = append(profiles, p)
			continue
		}
		if selection.Role != "implementation" && selection.Role != "review" {
			return errors.New("unsupported operator role")
		}
		p, err := workProfile(selection.Profile)
		if err != nil {
			return err
		}
		if selection.Role == "review" {
			rules, err := parseJobDraftPermissions(p.Permissions)
			if err != nil {
				return err
			}
			if err = verifyReviewerRuntime(repo, p.Agent, rules); err != nil {
				return err
			}
		}
		profiles = append(profiles, MainProfile{Agent: p.Agent, ProviderID: p.ProviderID, ModelID: p.ModelID, Variant: p.Variant})
	}
	service, err := discoverNativeService(repo)
	if err != nil {
		return err
	}
	body, err := nativeCall(repo, service, "get", "/api/model"+locationQuery(repo), nil)
	if err != nil {
		return err
	}
	var models struct {
		Location struct {
			Directory string `json:"directory"`
		} `json:"location"`
		Data []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Variants   []struct {
				ID string `json:"id"`
			} `json:"variants"`
			Enabled bool   `json:"enabled"`
			Type    string `json:"type"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &models) != nil || models.Location.Directory != repo || models.Data == nil {
		return errors.New("configured model catalog unavailable")
	}
	agents := map[string]bool{}
	for _, p := range profiles {
		if p.Variant == "" {
			return errors.New("operator selections require an explicit model variant")
		}
		found := false
		for _, model := range models.Data {
			if model.ID == p.ModelID && model.ProviderID == p.ProviderID && model.Enabled && (model.Type == "" || model.Type == "chat") {
				found = p.Variant == "default"
				for _, variant := range model.Variants {
					if variant.ID == p.Variant {
						found = true
					}
				}
			}
		}
		if !found {
			return errors.New("selected configured model/variant unavailable")
		}
		if agents[p.Agent] {
			continue
		}
		body, err = nativeCall(repo, service, "get", "/api/agent/"+url.PathEscape(p.Agent)+locationQuery(repo), nil)
		if err != nil {
			return err
		}
		var agent struct {
			Location struct {
				Directory string `json:"directory"`
			} `json:"location"`
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &agent) != nil || agent.Location.Directory != repo || agent.Data.ID != p.Agent {
			return errors.New("selected configured agent unavailable")
		}
		agents[p.Agent] = true
	}
	return nil
}

// VerifyOperatorDispatcher is a readiness observation, not execution completion.
// The owner runs ordinary foreground dispatch with ONE worker slot: persistent
// Main plus one worker/reviewer is at most two executions. Never broaden the
// frozen command-only native launcher to obtain agent execution.
func VerifyOperatorDispatcher(dir, executable string, owner Process) error {
	boot, bootErr := bootID()
	start, startErr := startTicks(owner.PID)
	if owner.PID <= 0 || bootErr != nil || startErr != nil || boot != owner.Boot || start != owner.Start {
		return errors.New("owned agent dispatcher is not alive")
	}
	available, err := memAvailable()
	if err != nil || available < 6<<30 {
		return errors.New("operator workflow requires at least 6 GiB available memory")
	}
	f, err := os.OpenFile(filepath.Join(dir, "dispatch.lock"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	var observed Process
	if json.NewDecoder(f).Decode(&observed) != nil || observed != owner {
		return errors.New("agent dispatcher lock owner changed")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		if err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
		return errors.New("agent dispatcher lock is not held")
	}
	argv, err := os.ReadFile("/proc/" + strconv.Itoa(owner.PID) + "/cmdline")
	want := []string{executable, "--state-dir", dir, "dispatch", "--slots", "1", "--memory-floor-bytes", "6442450944", ""}
	if err != nil || string(argv) != strings.Join(want, "\x00") {
		return errors.New("agent dispatcher must use the exact bounded foreground invocation")
	}
	actual, err := os.Readlink("/proc/" + strconv.Itoa(owner.PID) + "/exe")
	if err != nil || actual != executable {
		return errors.New("agent dispatcher executable changed")
	}
	_, _, expectedImage, err := dispatcherImage(executable)
	if err != nil {
		return err
	}
	_, _, runningImage, err := dispatcherImage("/proc/" + strconv.Itoa(owner.PID) + "/exe")
	if err != nil || expectedImage != runningImage {
		return errors.New("running agent dispatcher image changed")
	}
	environ, err := os.Open("/proc/" + strconv.Itoa(owner.PID) + "/environ")
	if err != nil {
		return err
	}
	defer environ.Close()
	raw, err := io.ReadAll(io.LimitReader(environ, 256<<10+1))
	if err != nil || len(raw) > 256<<10 {
		return errors.New("agent dispatcher environment unavailable")
	}
	context := func(entries []string) map[string]string {
		out := map[string]string{}
		for _, entry := range entries {
			key, value, ok := strings.Cut(entry, "=")
			if ok && (key == "HOME" || key == "PATH" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "OPENCODE_")) {
				out[key] = value
			}
		}
		return out
	}
	if !jsonEqual(context(strings.Split(string(raw), "\x00")), context(os.Environ())) {
		return errors.New("agent dispatcher discovery/configuration context differs from endpoint")
	}
	after, err := startTicks(owner.PID)
	if err != nil || after != owner.Start {
		return errors.New("agent dispatcher identity changed during readiness observation")
	}
	return nil
}
