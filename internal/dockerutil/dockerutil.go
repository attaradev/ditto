package dockerutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// NewClient resolves a reachable Docker-compatible daemon and returns a client
// configured for it.
func NewClient(ctx context.Context, configuredHost string) (*client.Client, string, error) {
	host, err := resolveHost(ctx, configuredHost, os.Getenv, probeHost)
	if err != nil {
		return nil, "", err
	}

	cli, err := newClientForHost(host)
	if err != nil {
		return nil, "", fmt.Errorf("docker runtime: create client for %s: %w", host, err)
	}
	return cli, host, nil
}

func resolveHost(
	ctx context.Context,
	configuredHost string,
	getEnv func(string) string,
	probe func(context.Context, string) error,
) (string, error) {
	if configuredHost != "" {
		if err := validateHost(configuredHost); err != nil {
			return "", fmt.Errorf("docker runtime: invalid docker_host %q: %w", configuredHost, err)
		}
		if err := probe(ctx, configuredHost); err != nil {
			return "", fmt.Errorf("docker runtime: docker_host=%s is not reachable: %w", configuredHost, err)
		}
		return configuredHost, nil
	}

	if envHost := getEnv("DOCKER_HOST"); envHost != "" {
		if err := validateHost(envHost); err != nil {
			return "", fmt.Errorf("docker runtime: invalid DOCKER_HOST %q: %w", envHost, err)
		}
		if err := probe(ctx, envHost); err != nil {
			return "", fmt.Errorf("docker runtime: DOCKER_HOST=%s is not reachable: %w", envHost, err)
		}
		return envHost, nil
	}

	if err := probe(ctx, client.DefaultDockerHost); err == nil {
		return client.DefaultDockerHost, nil
	}

	return "", fmt.Errorf(
		"docker runtime: no Docker-compatible daemon found — start Docker Desktop or another Docker-compatible runtime, or set DOCKER_HOST (e.g. export DOCKER_HOST=unix:///var/run/docker.sock)",
	)
}

func validateHost(host string) error {
	_, err := client.ParseHostURL(host)
	return err
}

func newClientForHost(host string) (*client.Client, error) {
	return client.NewClientWithOpts(
		client.WithHost(host),
		client.WithTLSClientConfigFromEnv(),
		client.WithVersionFromEnv(),
		client.WithAPIVersionNegotiation(),
	)
}

func probeHost(ctx context.Context, host string) error {
	cli, err := newClientForHost(host)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_, err = cli.Ping(pingCtx)
	return err
}

// EnsureImage pulls imageRef when it is not already present on the daemon.
func EnsureImage(ctx context.Context, cli *client.Client, imageRef string) error {
	if _, err := cli.ImageInspect(ctx, imageRef); err == nil {
		return nil
	}

	rc, err := cli.ImagePull(ctx, imageRef, imagetypes.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", imageRef, err)
	}
	defer func() { _ = rc.Close() }()

	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("read pull stream for %s: %w", imageRef, err)
	}
	return nil
}

// RunContainer runs a short-lived helper container and removes it afterwards.
// On failure it includes captured container output in the returned error.
func RunContainer(
	ctx context.Context,
	cli *client.Client,
	cfg *container.Config,
	hostCfg *container.HostConfig,
	name string,
) error {
	return RunContainerOnNetwork(ctx, cli, cfg, hostCfg, nil, name)
}

// RunContainerOnNetwork is like RunContainer but attaches the container to a
// named Docker network via netCfg. Pass nil for netCfg to use the default
// bridge network (equivalent to RunContainer).
func RunContainerOnNetwork(
	ctx context.Context,
	cli *client.Client,
	cfg *container.Config,
	hostCfg *container.HostConfig,
	netCfg *network.NetworkingConfig,
	name string,
) error {
	if cfg == nil {
		return fmt.Errorf("docker runtime: missing container config")
	}
	if cfg.Image == "" {
		return fmt.Errorf("docker runtime: missing container image")
	}

	if err := EnsureImage(ctx, cli, cfg.Image); err != nil {
		return err
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return fmt.Errorf("docker runtime: create helper container: %w", err)
	}
	defer func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("docker runtime: start helper container: %w", err)
	}

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("docker runtime: wait for helper container: %w", err)
		}
	case status := <-statusCh:
		if status.Error != nil {
			return fmt.Errorf("docker runtime: helper container failed: %s", status.Error.Message)
		}
		if status.StatusCode != 0 {
			logs, logErr := readContainerLogs(ctx, cli, resp.ID)
			if logErr != nil {
				return fmt.Errorf("docker runtime: helper container exited with status %d (failed to read logs: %v)", status.StatusCode, logErr)
			}
			logs = strings.TrimSpace(logs)
			if logs != "" {
				return fmt.Errorf("docker runtime: helper container exited with status %d: %s", status.StatusCode, logs)
			}
			return fmt.Errorf("docker runtime: helper container exited with status %d", status.StatusCode)
		}
	}

	return nil
}

func readContainerLogs(ctx context.Context, cli *client.Client, containerID string) (string, error) {
	rc, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, rc); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Exec runs cmd inside an existing container through the Docker SDK.
func Exec(ctx context.Context, cli *client.Client, containerID string, cmd []string, stdin io.Reader) error {
	if len(cmd) == 0 {
		return fmt.Errorf("docker runtime: missing exec command")
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdin:  stdin != nil,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	})
	if err != nil {
		return fmt.Errorf("docker runtime: create exec %q: %w", strings.Join(cmd, " "), err)
	}

	attach, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("docker runtime: attach exec %q: %w", strings.Join(cmd, " "), err)
	}
	defer attach.Close()

	stdinErrCh := make(chan error, 1)
	if stdin != nil {
		go func() {
			_, err := io.Copy(attach.Conn, stdin)
			if err == nil {
				err = attach.CloseWrite()
			}
			stdinErrCh <- err
		}()
	}

	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, attach.Reader); err != nil {
		return fmt.Errorf("docker runtime: read exec output for %q: %w", strings.Join(cmd, " "), err)
	}

	if stdin != nil {
		if err := <-stdinErrCh; err != nil {
			return fmt.Errorf("docker runtime: stream stdin to %q: %w", strings.Join(cmd, " "), err)
		}
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("docker runtime: inspect exec %q: %w", strings.Join(cmd, " "), err)
	}
	if inspect.ExitCode != 0 {
		msg := strings.TrimSpace(output.String())
		if msg != "" {
			return fmt.Errorf("docker runtime: exec %q failed with exit code %d: %s", strings.Join(cmd, " "), inspect.ExitCode, msg)
		}
		return fmt.Errorf("docker runtime: exec %q failed with exit code %d", strings.Join(cmd, " "), inspect.ExitCode)
	}

	return nil
}
