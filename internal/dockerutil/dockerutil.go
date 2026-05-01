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
	if host, err := resolvePreferredHost(ctx, configuredHost, "docker_host", probe); host != "" || err != nil {
		return host, err
	}

	if host, err := resolvePreferredHost(ctx, getEnv("DOCKER_HOST"), "DOCKER_HOST", probe); host != "" || err != nil {
		return host, err
	}

	if err := probe(ctx, client.DefaultDockerHost); err == nil {
		return client.DefaultDockerHost, nil
	}

	return "", fmt.Errorf(
		"docker runtime: no Docker-compatible daemon found — start Docker Desktop or another Docker-compatible runtime, or set DOCKER_HOST (e.g. export DOCKER_HOST=unix:///var/run/docker.sock)",
	)
}

func resolvePreferredHost(
	ctx context.Context,
	host string,
	label string,
	probe func(context.Context, string) error,
) (string, error) {
	if host == "" {
		return "", nil
	}
	if err := validateHost(host); err != nil {
		return "", fmt.Errorf("docker runtime: invalid %s %q: %w", label, host, err)
	}
	if err := probe(ctx, host); err != nil {
		if label == "DOCKER_HOST" {
			return "", fmt.Errorf("docker runtime: DOCKER_HOST=%s is not reachable: %w", host, err)
		}
		return "", fmt.Errorf("docker runtime: %s=%s is not reachable: %w", label, host, err)
	}
	return host, nil
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

// RunRequest describes a short-lived helper container execution.
type RunRequest struct {
	Config        *container.Config
	HostConfig    *container.HostConfig
	NetworkConfig *network.NetworkingConfig
	Name          string
	Stdin         io.Reader
}

// RunContainer runs a short-lived helper container and removes it afterwards.
// On failure it includes captured container output in the returned error.
func RunContainer(ctx context.Context, cli *client.Client, req RunRequest) error {
	if err := validateRunRequest(req); err != nil {
		return err
	}
	if err := EnsureImage(ctx, cli, req.Config.Image); err != nil {
		return err
	}

	cfgCopy := copyContainerConfig(req.Config, req.Stdin != nil)
	resp, err := cli.ContainerCreate(ctx, cfgCopy, req.HostConfig, req.NetworkConfig, nil, req.Name)
	if err != nil {
		return fmt.Errorf("docker runtime: create helper container: %w", err)
	}
	defer func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	}()

	stdinErrCh, releaseAttach, err := attachContainerInput(ctx, cli, resp.ID, req.Stdin)
	if err != nil {
		return err
	}
	defer releaseAttach()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("docker runtime: start helper container: %w", err)
	}
	if err := waitForContainerSuccess(ctx, cli, resp.ID); err != nil {
		return err
	}
	if err := waitForHelperStdinStream(stdinErrCh); err != nil {
		return err
	}
	return nil
}

func validateRunRequest(req RunRequest) error {
	if req.Config == nil {
		return fmt.Errorf("docker runtime: missing container config")
	}
	if req.Config.Image == "" {
		return fmt.Errorf("docker runtime: missing container image")
	}
	return nil
}

func copyContainerConfig(cfg *container.Config, withStdin bool) *container.Config {
	cfgCopy := *cfg
	if withStdin {
		cfgCopy.OpenStdin = true
		cfgCopy.AttachStdin = true
	}
	return &cfgCopy
}

func attachContainerInput(ctx context.Context, cli *client.Client, containerID string, stdin io.Reader) (chan error, func(), error) {
	if stdin == nil {
		return nil, func() {}, nil
	}
	attach, err := cli.ContainerAttach(ctx, containerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("docker runtime: attach helper container stdin: %w", err)
	}
	return streamInputToAttach(attach.Conn, attach.CloseWrite, stdin), attach.Close, nil
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

// ExecRequest describes a command execution inside a running container.
type ExecRequest struct {
	ContainerID string
	Command     []string
	Stdin       io.Reader
}

// Exec runs cmd inside an existing container through the Docker SDK.
func Exec(ctx context.Context, cli *client.Client, req ExecRequest) error {
	if len(req.Command) == 0 {
		return fmt.Errorf("docker runtime: missing exec command")
	}
	cmdLabel := strings.Join(req.Command, " ")

	execID, err := createExecCommand(ctx, cli, req)
	if err != nil {
		return fmt.Errorf("docker runtime: create exec %q: %w", cmdLabel, err)
	}

	output, err := collectExecOutput(execOutputInput{
		ctx:      ctx,
		cli:      cli,
		execID:   execID,
		stdin:    req.Stdin,
		cmdLabel: cmdLabel,
	})
	if err != nil {
		return err
	}

	return inspectExecResult(execInspectInput{
		ctx:      ctx,
		cli:      cli,
		execID:   execID,
		cmdLabel: cmdLabel,
		output:   output,
	})
}

func createExecCommand(ctx context.Context, cli *client.Client, req ExecRequest) (string, error) {
	execResp, err := cli.ContainerExecCreate(ctx, req.ContainerID, container.ExecOptions{
		AttachStdin:  req.Stdin != nil,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          req.Command,
	})
	if err != nil {
		return "", err
	}
	return execResp.ID, nil
}

type execOutputInput struct {
	ctx      context.Context
	cli      *client.Client
	execID   string
	stdin    io.Reader
	cmdLabel string
}

func collectExecOutput(in execOutputInput) (string, error) {
	attach, err := in.cli.ContainerExecAttach(in.ctx, in.execID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("docker runtime: attach exec %q: %w", in.cmdLabel, err)
	}
	defer attach.Close()

	var stdinErrCh chan error
	if in.stdin != nil {
		stdinErrCh = streamInputToAttach(attach.Conn, attach.CloseWrite, in.stdin)
	}

	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, attach.Reader); err != nil {
		return "", fmt.Errorf("docker runtime: read exec output for %q: %w", in.cmdLabel, err)
	}
	if err := waitForExecStdinStream(stdinErrCh, in.cmdLabel); err != nil {
		return "", err
	}
	return output.String(), nil
}

type execInspectInput struct {
	ctx      context.Context
	cli      *client.Client
	execID   string
	cmdLabel string
	output   string
}

func inspectExecResult(in execInspectInput) error {
	inspect, err := in.cli.ContainerExecInspect(in.ctx, in.execID)
	if err != nil {
		return fmt.Errorf("docker runtime: inspect exec %q: %w", in.cmdLabel, err)
	}
	if inspect.ExitCode == 0 {
		return nil
	}
	msg := strings.TrimSpace(in.output)
	if msg != "" {
		return fmt.Errorf("docker runtime: exec %q failed with exit code %d: %s", in.cmdLabel, inspect.ExitCode, msg)
	}
	return fmt.Errorf("docker runtime: exec %q failed with exit code %d", in.cmdLabel, inspect.ExitCode)
}

func waitForHelperStdinStream(stdinErrCh chan error) error {
	err := readStdinStreamError(stdinErrCh)
	if err == nil {
		return nil
	}
	return fmt.Errorf("docker runtime: stream stdin to helper container: %w", err)
}

func waitForExecStdinStream(stdinErrCh chan error, cmdLabel string) error {
	err := readStdinStreamError(stdinErrCh)
	if err == nil {
		return nil
	}
	return fmt.Errorf("docker runtime: stream stdin to %q: %w", cmdLabel, err)
}

func readStdinStreamError(stdinErrCh chan error) error {
	if stdinErrCh == nil {
		return nil
	}
	return <-stdinErrCh
}

func streamInputToAttach(dst io.Writer, closeWrite func() error, src io.Reader) chan error {
	errCh := make(chan error, 1)
	go func() {
		_, err := io.Copy(dst, src)
		if err == nil {
			err = closeWrite()
		}
		errCh <- err
	}()
	return errCh
}

func waitForContainerSuccess(ctx context.Context, cli *client.Client, containerID string) error {
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("docker runtime: wait for helper container: %w", err)
		}
		return nil
	case status := <-statusCh:
		if status.Error != nil {
			return fmt.Errorf("docker runtime: helper container failed: %s", status.Error.Message)
		}
		if status.StatusCode == 0 {
			return nil
		}
		return helperContainerExitError(ctx, cli, containerID, status.StatusCode)
	}
}

func helperContainerExitError(ctx context.Context, cli *client.Client, containerID string, statusCode int64) error {
	logs, logErr := readContainerLogs(ctx, cli, containerID)
	if logErr != nil {
		return fmt.Errorf("docker runtime: helper container exited with status %d (failed to read logs: %v)", statusCode, logErr)
	}
	logs = strings.TrimSpace(logs)
	if logs != "" {
		return fmt.Errorf("docker runtime: helper container exited with status %d: %s", statusCode, logs)
	}
	return fmt.Errorf("docker runtime: helper container exited with status %d", statusCode)
}
