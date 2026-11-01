package aiops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshConnectionAttempts = 3
const sshConnectionPhaseTimeout = 10 * time.Second

var errSSHHostKeyMismatch = errors.New("SSH 主机指纹不匹配，连接已拒绝")

// Only failures from connection/session setup enter this classifier. Never
// classify a session.Run error as retryable: exec may already have happened.
type sshConnectionFailure struct {
	stage     string
	code      string
	retryable bool
	attempts  int
}

func (e *sshConnectionFailure) Error() string {
	switch e.code {
	case "host_key":
		return "SSH 主机指纹不匹配，连接已拒绝；命令未发送"
	case "authentication":
		return "SSH 认证失败，命令未发送"
	case "protocol":
		return "SSH 协议或算法协商失败，命令未发送"
	case "canceled":
		return "SSH 连接等待已停止或总时限已到，命令未发送"
	case "authorization":
		return "SSH 操作已停止或授权无法确认，命令未发送"
	}
	stage := "建立连接"
	if e.stage == "handshake" {
		stage = "握手"
	} else if e.stage == "session" {
		stage = "创建会话"
	}
	reason := "失败"
	if e.code == "timeout" {
		reason = "超时"
	} else if e.code == "network" {
		reason = "网络连接中断或暂时不可达"
	}
	if e.attempts > 1 {
		return fmt.Sprintf("SSH %s阶段%s，已尝试 %d 次连接；命令未发送", stage, reason, e.attempts)
	}
	return fmt.Sprintf("SSH %s阶段%s，命令未发送", stage, reason)
}

func classifySSHConnectionFailure(ctx context.Context, stage string, err error) *sshConnectionFailure {
	failure := &sshConnectionFailure{stage: stage, code: "setup"}
	switch {
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		failure.code = "canceled"
	case errors.Is(err, errSSHHostKeyMismatch):
		failure.code = "host_key"
	// x/crypto/ssh v0.49 has no exported client authentication error type.
	// Recognize only its fixed prefix; never expose server-controlled details.
	case strings.HasPrefix(strings.TrimPrefix(err.Error(), "ssh: handshake failed: "), "ssh: unable to authenticate"):
		failure.code = "authentication"
	default:
		var negotiation *ssh.AlgorithmNegotiationError
		var dns *net.DNSError
		var network net.Error
		switch {
		case errors.As(err, &negotiation) || strings.Contains(err.Error(), "ssh: no common public key signature algorithm") || strings.Contains(err.Error(), "ssh: unsupported version"):
			failure.code = "protocol"
		case errors.As(err, &dns) && !dns.IsTimeout && !dns.IsTemporary:
			// A nonexistent/malformed destination will not improve on replay.
		case errors.As(err, &network) && network.Timeout():
			failure.code, failure.retryable = "timeout", true
		case errors.As(err, &network) && network.Temporary():
			failure.code, failure.retryable = "network", true
		case stage == "session" && err.Error() == "ssh: unexpected packet in response to channel open: <nil>":
			// x/crypto/ssh v0.49 loses the underlying EOF when mux teardown
			// closes ch.msg while NewSession is waiting for channel-open.
			// Match only that exact local error before exec, not arbitrary
			// protocol/remote error text containing "unexpected packet".
			failure.code, failure.retryable = "network", true
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed),
			errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED), errors.Is(err, syscall.ECONNREFUSED),
			errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ETIMEDOUT), errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
			failure.code, failure.retryable = "network", true
		}
	}
	return failure
}

// Open a session but do NOT send an exec request. Every failed attempt owns and
// closes its transport. On success cleanup retains the context cancellation hook
// through command execution, so stopping a turn also closes the active socket.
func openSSHSession(ctx context.Context, v sshSecretServer, auth []ssh.AuthMethod) (*ssh.Session, func(), string, error) {
	address := net.JoinHostPort(v.Host, strconv.Itoa(v.Port))
	conn, err := (&net.Dialer{Timeout: sshConnectionPhaseTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, "dial", err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cleanup := func() { stop(); _ = conn.Close() }
	deadline := time.Now().Add(sshConnectionPhaseTimeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = conn.SetDeadline(deadline)
	cc, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: v.Username, Auth: auth, HostKeyCallback: pinnedHostKey(v.HostKeySHA256), Timeout: sshConnectionPhaseTimeout})
	if err != nil {
		cleanup()
		return nil, nil, "handshake", err
	}
	client := ssh.NewClient(cc, channels, requests)
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		cleanup()
		return nil, nil, "session", err
	}
	// The total action deadline is shared by retries and command execution.
	deadline, _ = ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	return session, func() { _ = session.Close(); _ = client.Close(); cleanup() }, "", nil
}

func connectSSHSession(ctx context.Context, v sshSecretServer, auth []ssh.AuthMethod, report func(string), guard func(context.Context) error) (*ssh.Session, func(), error) {
	for attempt := 1; attempt <= sshConnectionAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, nil, &sshConnectionFailure{code: "canceled"}
		}
		if guard != nil && guard(ctx) != nil {
			return nil, nil, &sshConnectionFailure{code: "authorization"}
		}
		session, cleanup, stage, err := openSSHSession(ctx, v, auth)
		if err == nil {
			if attempt > 1 {
				report(fmt.Sprintf("[SSH] 自动重连成功（第 %d/%d 次连接）。", attempt, sshConnectionAttempts))
			}
			return session, cleanup, nil
		}
		failure := classifySSHConnectionFailure(ctx, stage, err)
		failure.attempts = attempt
		if !failure.retryable || attempt == sshConnectionAttempts {
			return nil, nil, failure
		}
		// Bounded backoff (1s, 2s) within the original action lifetime. The
		// command has not left this process, so reconnecting cannot replay it.
		wait := time.Duration(attempt) * time.Second
		report(fmt.Sprintf("[SSH] 本次连接暂时失败，命令尚未发送；%d 秒后自动重连（第 %d/%d 次连接）。", attempt, attempt+1, sshConnectionAttempts))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, &sshConnectionFailure{code: "canceled"}
		case <-timer.C:
		}
	}
	panic("unreachable SSH connection retry state")
}
