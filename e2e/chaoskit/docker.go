package chaoskit

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Container is one Docker container a test started. It is only ever
// addressed by the ID docker run returned, so a test never touches a
// container someone else started (LOOP.md: only kill what you started).
type Container struct {
	ID      string
	Context string // docker --context ("" for the current one)
}

// DockerContext is the docker context the loops use: TW_DOCKER_CONTEXT,
// else PG_CTX (e2e/multimgmt's), else the current context.
func DockerContext() string {
	if c := os.Getenv("TW_DOCKER_CONTEXT"); c != "" {
		return c
	}
	return os.Getenv("PG_CTX")
}

func docker(ctx string, args ...string) ([]byte, error) {
	if ctx != "" {
		args = append([]string{"--context", ctx}, args...)
	}
	return exec.Command("docker", args...).CombinedOutput()
}

// HasDocker reports whether the docker CLI can reach a daemon in ctx.
func HasDocker(ctx string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return err
	}
	if out, err := docker(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("docker: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Run starts a detached container (docker run -d --rm args...).
func Run(ctx string, args ...string) (*Container, error) {
	out, err := docker(ctx, append([]string{"run", "-d", "--rm"}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	id := strings.TrimSpace(lines[len(lines)-1])
	if len(id) < 12 {
		return nil, fmt.Errorf("docker run: no container ID in %q", out)
	}
	return &Container{ID: id, Context: ctx}, nil
}

// Exec runs a command in the container.
func (c *Container) Exec(args ...string) ([]byte, error) {
	return docker(c.Context, append([]string{"exec", c.ID}, args...)...)
}

// Port is the host address a published container port (e.g. "3900/tcp")
// is bound to.
func (c *Container) Port(port string) (string, error) {
	out, err := docker(c.Context, "port", c.ID, port)
	if err != nil {
		return "", fmt.Errorf("docker port: %v: %s", err, out)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(l, "127.0.0.1:") {
			return strings.TrimSpace(l), nil
		}
	}
	return "", fmt.Errorf("docker port %s: %q", port, out)
}

// Logs is the container's output so far.
func (c *Container) Logs() []byte {
	out, _ := docker(c.Context, "logs", c.ID)
	return out
}

// Remove force-removes the container (by ID).
func (c *Container) Remove() {
	docker(c.Context, "rm", "-f", c.ID)
}
