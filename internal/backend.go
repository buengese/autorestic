package internal

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/cupcakearmy/autorestic/internal/colors"
)

type BackendRest struct {
	User     string `yaml:"user,omitempty"`
	Password string `yaml:"password,omitempty"`
}

type Backend struct {
	name    string
	Type    string            `yaml:"type,omitempty"`
	Path    string            `yaml:"path,omitempty"`
	Key     string            `yaml:"key,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Rest    BackendRest       `yaml:"rest,omitempty"`
	Options Options           `yaml:"options,omitempty"`
}

func GetBackend(name string) (Backend, bool) {
	b, ok := GetConfig().Backends[name]
	b.name = name
	return b, ok
}

func (b Backend) generateRepo() (string, error) {
	switch b.Type {
	case "local":
		return GetPathRelativeToConfig(b.Path)
	case "rest":
		parsed, err := url.Parse(b.Path)
		if err != nil {
			return "", err
		}
		if b.Rest.User != "" {
			if b.Rest.Password == "" {
				parsed.User = url.User(b.Rest.User)
			} else {
				parsed.User = url.UserPassword(b.Rest.User, b.Rest.Password)
			}
		}
		return fmt.Sprintf("%s:%s", b.Type, parsed.String()), nil
	case "b2", "azure", "gs", "s3", "sftp", "rclone":
		return fmt.Sprintf("%s:%s", b.Type, b.Path), nil
	default:
		return "", fmt.Errorf("backend type \"%s\" is invalid", b.Type)
	}
}

func (b Backend) getEnv() (map[string]string, error) {
	env := make(map[string]string)
	// Key
	if b.Key != "" {
		env["RESTIC_PASSWORD"] = b.Key
	}

	// From config file
	repo, err := b.generateRepo()
	env["RESTIC_REPOSITORY"] = repo
	for key, value := range b.Env {
		env[strings.ToUpper(key)] = value
	}

	// From Envfile and passed as env
	var prefix = "AUTORESTIC_" + strings.ToUpper(b.name) + "_"
	for _, variable := range os.Environ() {
		var splitted = strings.SplitN(variable, "=", 2)
		if strings.HasPrefix(splitted[0], prefix) {
			env[strings.TrimPrefix(splitted[0], prefix)] = splitted[1]
		}
	}

	return env, err
}

func generateRandomKey() string {
	b := make([]byte, 64)
	rand.Read(b)
	key := base64.StdEncoding.EncodeToString(b)
	key = strings.ReplaceAll(key, "=", "")
	key = strings.ReplaceAll(key, "+", "")
	key = strings.ReplaceAll(key, "/", "")
	return key
}

func (b Backend) validate() error {
	if b.Type == "" {
		return fmt.Errorf(`Backend "%s" has no "type"`, b.name)
	}
	if b.Path == "" {
		return fmt.Errorf(`Backend "%s" has no "path"`, b.name)
	}
	if b.Key == "" {
		// Check if key is set in environment
		env, _ := b.getEnv()
		if _, found := env["RESTIC_PASSWORD"]; !found {
			// No key set in config file or env => generate random key and save file
			key := generateRandomKey()
			b.Key = key
			c := GetConfig()
			tmp := c.Backends[b.name]
			tmp.Key = key
			c.Backends[b.name] = tmp
			if err := c.SaveConfig(); err != nil {
				return err
			}
		}
	}
	env, err := b.getEnv()
	if err != nil {
		return err
	}
	options := ExecuteOptions{Envs: env}
	// Check if already initialized
	_, err = ExecuteResticCommand(options, "snapshots")
	if err == nil {
		return nil
	} else {
		// If not initialize
		colors.Body.Printf("Initializing backend \"%s\"...\n", b.name)
		out, err := ExecuteResticCommand(options, "init")
		if VERBOSE {
			colors.Faint.Println(out)
		}
		return err
	}
}

func (b Backend) Exec(args []string) error {
	env, err := b.getEnv()
	if err != nil {
		return err
	}
	options := ExecuteOptions{Envs: env}
	out, err := ExecuteResticCommand(options, args...)
	if err != nil {
		colors.Error.Println(out)
		return err
	}
	if VERBOSE {
		colors.Faint.Println(out)
	}
	return nil
}

func (b Backend) ExecDocker(l Location, args []string) (string, error) {
	env, err := b.getEnv()
	if err != nil {
		return "", err
	}
	volume := l.From[0]
	options := ExecuteOptions{
		Command: "docker",
		Envs:    env,
	}
	dir := "/data"
	args = append([]string{"restic"}, args...)
	docker := []string{
		"run", "--rm",
		"--pull", "always",
		"--entrypoint", "ash",
		"--workdir", dir,
		"--volume", volume + ":" + dir,
	}
	// Use of docker host, not the container host
	if hostname, err := os.Hostname(); err == nil {
		docker = append(docker, "--hostname", hostname)
	}
	switch b.Type {
	case "local":
		actual := env["RESTIC_REPOSITORY"]
		docker = append(docker, "--volume", actual+":"+"/repo")
		env["RESTIC_REPOSITORY"] = "/repo"
	case "b2":
	case "s3":
	case "azure":
	case "gs":
		// No additional setup needed
	case "rclone":
		// Read host rclone config and mount it into the container
		configFile, err := ExecuteCommand(ExecuteOptions{Command: "rclone"}, "config", "file")
		if err != nil {
			return "", err
		}
		splitted := strings.Split(strings.TrimSpace(configFile), "\n")
		configFilePath := splitted[len(splitted)-1]
		docker = append(docker, "--volume", configFilePath+":"+"/root/.config/rclone/rclone.conf:ro")
	default:
		return "", fmt.Errorf("Backend type \"%s\" is not supported as volume endpoint", b.Type)
	}
	for key, value := range env {
		docker = append(docker, "--env", key+"="+value)
	}
	docker = append(docker, "cupcakearmy/autorestic:"+VERSION, "-c", strings.Join(args, " "))
	out, err := ExecuteCommand(options, docker...)
	return out, err
}
