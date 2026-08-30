package jira

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/rajpopat27/relay-flow/internal/config"
	jirarest "github.com/rajpopat27/relay-flow/internal/task/jira/rest"
	"gopkg.in/yaml.v3"
)

type credentials struct {
	Site  string `yaml:"site"`
	Email string `yaml:"email"`
	Token string `yaml:"token"`
}

func auth(ctx context.Context, args []string, stdin io.Reader) error {
	fs := flag.NewFlagSet("task auth", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	site := fs.String("site", "", "Jira site URL")
	email := fs.String("email", "", "Jira account email")
	token := fs.String("token", "", "Jira API token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	values := credentials{Site: strings.TrimSpace(*site), Email: strings.TrimSpace(*email), Token: *token}
	flagged := values.Site != "" || values.Email != "" || values.Token != ""
	if flagged && (values.Site == "" || values.Email == "" || values.Token == "") {
		return errors.New("--site, --email, and --token must be given together")
	}
	if !flagged {
		if isTTY(stdin) {
			var err error
			values, err = promptCredentials(values)
			if err != nil {
				return err
			}
		} else {
			var ok bool
			values, ok = readCredentialLines(stdin)
			if !ok {
				return errors.New("expected Jira site, email, and API token on stdin (or pass --site, --email, and --token)")
			}
		}
	}

	client, err := jirarest.New(values.Site, values.Email, values.Token)
	if err != nil {
		return err
	}
	if err := client.ValidateCredentials(ctx); err != nil {
		return err
	}
	values.Site = strings.TrimRight(clientSite(values.Site), "/")
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	return saveCredentials(path, values)
}

func isTTY(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

func promptCredentials(values credentials) (credentials, error) {
	required := func(name string) func(string) error {
		return func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", name)
			}
			return nil
		}
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Jira site").Description("Your Atlassian site URL.").Placeholder("https://company.atlassian.net").Validate(required("Jira site")).Value(&values.Site),
		huh.NewInput().Title("Jira email").Description("Email for the Jira API token.").Placeholder("you@company.com").Validate(required("Jira email")).Value(&values.Email),
		huh.NewInput().Title("Jira API token").Description("Stored locally and never displayed.").EchoMode(huh.EchoModePassword).Validate(required("Jira API token")).Value(&values.Token),
	).Title("Configure Jira"))
	if err := form.Run(); err != nil {
		return credentials{}, err
	}
	values.Site = strings.TrimSpace(values.Site)
	values.Email = strings.TrimSpace(values.Email)
	return values, nil
}

func readCredentialLines(stdin io.Reader) (credentials, bool) {
	values := make([]string, 0, 3)
	scanner := bufio.NewScanner(stdin)
	for len(values) < 3 && scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			values = append(values, line)
		}
	}
	if len(values) != 3 {
		return credentials{}, false
	}
	return credentials{Site: values[0], Email: values[1], Token: values[2]}, true
}

func clientSite(site string) string {
	if !strings.Contains(site, "://") {
		return "https://" + site
	}
	return site
}

func credentialsPath() (string, error) {
	if root := os.Getenv("RELAY_FLOW_HOME"); root != "" {
		return filepath.Join(root, "credentials.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".relay-flow", "credentials.yaml"), nil
}

func loadCredentialsDefault() (credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return credentials{}, err
	}
	return loadCredentials(path)
}

func loadCredentials(path string) (credentials, error) {
	info, err := os.Stat(path)
	if err != nil {
		return credentials{}, fmt.Errorf("stat credentials %s: %w", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		return credentials{}, fmt.Errorf("credentials %s must have mode 0600", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, fmt.Errorf("read credentials %s: %w", path, err)
	}
	var out credentials
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&out); err != nil {
		return credentials{}, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	if out.Site == "" || out.Email == "" || out.Token == "" {
		return credentials{}, errors.New("Jira site, email, and API token are required")
	}
	return out, nil
}

func saveCredentials(path string, value credentials) error {
	if value.Site == "" || value.Email == "" || value.Token == "" {
		return errors.New("Jira site, email, and API token are required")
	}
	raw, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	if err := config.WriteAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}
