package main

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

// waitForExit runs cmd and waits up to timeout for it to exit on its own,
// rather than blocking forever if it doesn't -- which would hang the test
// suite instead of failing it when a regression makes grpcmcp start serving
// where it used to reject the flags and exit.
func waitForExit(t *testing.T, cmd *exec.Cmd, timeout time.Duration) (exited bool, err error) {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return true, err
	case <-time.After(timeout):
		return false, nil
	}
}

// TestForwardHeaderRequiresHostport checks that -forward-header (and
// -forward-operator-identity) are rejected without -hostport, rather than
// silently doing nothing: stdio has no inbound HTTP headers to forward, so
// an operator relying on either flag for identity attribution would
// otherwise get no error and no forwarded header.
func TestForwardHeaderRequiresHostport(t *testing.T) {
	bin := buildGrpcmcp(t)
	descFile := emptyDescriptorFile(t)

	for _, flagName := range []string{"--forward-header=X-Forwarded-User", "--forward-operator-identity"} {
		t.Run(flagName, func(t *testing.T) {
			cmd := exec.Command(bin, flagName, "--descriptors="+descFile)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			exited, err := waitForExit(t, cmd, 2*time.Second)
			if !exited {
				t.Fatalf("expected the process to exit without -hostport; it is still running instead of rejecting the flag")
			}
			if err == nil {
				t.Fatalf("expected a non-zero exit without -hostport, got success")
			}
			if !bytes.Contains(stderr.Bytes(), []byte("need -hostport")) {
				t.Errorf("stderr = %q, want a message naming the -hostport requirement", stderr.String())
			}
		})
	}
}

// TestForwardHeaderCollisionRejected checks that -forward-header naming a
// header already set via -header (most dangerously Authorization) is
// rejected at startup, rather than letting an inbound MCP client silently
// override grpcmcp's own trusted backend credential.
func TestForwardHeaderCollisionRejected(t *testing.T) {
	bin := buildGrpcmcp(t)
	descFile := emptyDescriptorFile(t)

	cmd := exec.Command(bin,
		"--hostport=localhost:0",
		"--header=Authorization: Bearer backend-secret",
		"--forward-header=Authorization",
		"--descriptors="+descFile,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	exited, err := waitForExit(t, cmd, 2*time.Second)
	if !exited {
		t.Fatalf("expected the process to reject a colliding -forward-header; it is still running instead")
	}
	if err == nil {
		t.Fatalf("expected a non-zero exit for a colliding -forward-header, got success")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("collides")) {
		t.Errorf("stderr = %q, want a message naming the collision", stderr.String())
	}
}

// TestForwardOperatorIdentityCollisionRejected mirrors
// TestForwardHeaderCollisionRejected for -forward-operator-identity, whose
// forwarded header name (X-Operator-Identity) is fixed rather than
// configurable.
func TestForwardOperatorIdentityCollisionRejected(t *testing.T) {
	bin := buildGrpcmcp(t)
	descFile := emptyDescriptorFile(t)

	cmd := exec.Command(bin,
		"--hostport=localhost:0",
		"--header=X-Operator-Identity: someone-else",
		"--forward-operator-identity",
		"--descriptors="+descFile,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	exited, err := waitForExit(t, cmd, 2*time.Second)
	if !exited {
		t.Fatalf("expected the process to reject a colliding -forward-operator-identity; it is still running instead")
	}
	if err == nil {
		t.Fatalf("expected a non-zero exit for a colliding -forward-operator-identity, got success")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("collides")) {
		t.Errorf("stderr = %q, want a message naming the collision", stderr.String())
	}
}

// TestForwardHeaderNoCollisionStarts checks that the collision guard does not
// reject a -forward-header configuration with no actual collision.
func TestForwardHeaderNoCollisionStarts(t *testing.T) {
	bin := buildGrpcmcp(t)
	descFile := emptyDescriptorFile(t)

	cmd := exec.Command(bin,
		"--hostport=localhost:0",
		"--header=Authorization: Bearer backend-secret",
		"--forward-header=X-Forwarded-User",
		"--descriptors="+descFile,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	exited, err := waitForExit(t, cmd, 1*time.Second)
	if exited {
		t.Fatalf("process exited, expected it to keep serving: %v, stderr: %s", err, stderr.String())
	}
	// Still running after the timeout, as expected: no collision, nothing to reject.
}
