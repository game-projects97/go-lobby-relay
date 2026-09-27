package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var cliTestToken = func() (token [32]byte) {
	for index := range token {
		token[index] = 'K'
	}
	return token
}()

func TestParseConfigRequiresExactFlagsAndWiresToken(t *testing.T) {
	tokenPath := writeTokenFile(t, "\n")
	args := validArgs(tokenPath)
	config, err := parseConfig(args)
	if err != nil {
		t.Fatalf("parseConfig(valid): %v", err)
	}
	if config.OperatorListen != "127.0.0.1:0" || config.PlayerListen != "127.0.0.1:0" || config.RelayNetwork != "udp4" ||
		config.RelayListen != "127.0.0.1:0" || config.AdvertisedHost != "relay.test" ||
		config.AdvertisedPort != 30000 || config.OperatorToken != cliTestToken {
		t.Fatalf("parseConfig(valid) = %#v", config)
	}

	flags := []string{
		"--management-listen", "--player-listen", "--relay-network", "--relay-listen",
		"--advertised-host", "--advertised-port", "--operator-token-file",
	}
	for _, flagName := range flags {
		t.Run("missing "+flagName, func(t *testing.T) {
			if _, err := parseConfig(removeFlag(args, flagName)); err == nil {
				t.Fatalf("missing %s was accepted", flagName)
			}
		})
		t.Run("duplicate "+flagName, func(t *testing.T) {
			value := flagValue(args, flagName)
			if _, err := parseConfig(append(append([]string(nil), args...), flagName, value)); err == nil {
				t.Fatalf("duplicate %s was accepted", flagName)
			}
		})
	}

	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", append(append([]string(nil), args...), "--unknown", "value")},
		{"missing value", append([]string(nil), args[:len(args)-1]...)},
		{"positional argument", append(append([]string(nil), args...), "extra")},
		{"single-dash spelling", append([]string{"-management-listen", "127.0.0.1:0"}, args[2:]...)},
		{"zero advertised port", replaceFlag(args, "--advertised-port", "0")},
		{"advertised port over uint16", replaceFlag(args, "--advertised-port", "65536")},
		{"nonnumeric advertised port", replaceFlag(args, "--advertised-port", "port")},
		{"invalid relay network", replaceFlag(args, "--relay-network", "udp")},
		{"relative token path", replaceFlag(args, "--operator-token-file", "token")},
		{"empty management listen", replaceFlag(args, "--management-listen", "")},
		{"empty player listen", replaceFlag(args, "--player-listen", "")},
		{"empty relay listen", replaceFlag(args, "--relay-listen", "")},
		{"empty advertised host", replaceFlag(args, "--advertised-host", "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseConfig(tt.args); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestReadOperatorTokenFileContract(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run("accepted suffix "+strings.ReplaceAll(suffix, "\n", "LF"), func(t *testing.T) {
			path := writeTokenFile(t, suffix)
			if got, err := readOperatorToken(path); err != nil || got != cliTestToken {
				t.Fatalf("readOperatorToken() = (%x, %v)", got, err)
			}
		})
	}

	valid := base64.RawURLEncoding.EncodeToString(cliTestToken[:])
	zero := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	noncanonical := valid[:len(valid)-1] + "t"
	invalidBodies := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"short", valid[:len(valid)-1]},
		{"long", valid + "A"},
		{"invalid alphabet", strings.Repeat("!", 43)},
		{"noncanonical trailing bits", noncanonical},
		{"all zero", zero},
		{"trailing CR", valid + "\r"},
		{"two trailing LF", valid + "\n\n"},
		{"trailing space", valid + " "},
		{"over bounded read", valid + "\r\nX"},
	}
	for _, tt := range invalidBodies {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("WriteFile(): %v", err)
			}
			if token, err := readOperatorToken(path); err == nil || token != ([32]byte{}) {
				t.Fatalf("readOperatorToken() = (%x, %v), want zero/error", token, err)
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		if _, err := readOperatorToken(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("missing token file was accepted")
		}
	})
	t.Run("nonregular", func(t *testing.T) {
		if _, err := readOperatorToken(t.TempDir()); err == nil {
			t.Fatal("directory token path was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target := writeTokenFile(t, "")
		link := filepath.Join(t.TempDir(), "token-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(): %v", err)
		}
		if _, err := readOperatorToken(link); err == nil {
			t.Fatal("symlink token path was accepted")
		}
	})
	for _, mode := range []os.FileMode{0o400, 0o640, 0o604, 0o666} {
		t.Run("mode "+mode.String(), func(t *testing.T) {
			path := writeTokenFile(t, "")
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("Chmod(): %v", err)
			}
			if _, err := readOperatorToken(path); err == nil {
				t.Fatalf("mode %04o was accepted", mode.Perm())
			}
		})
	}

	t.Run("ambiguous replacement", func(t *testing.T) {
		original := writeTokenFile(t, "")
		replacement := writeTokenFile(t, "")
		if _, err := readOperatorTokenWith(original, func(string) (*os.File, error) {
			return os.Open(replacement)
		}); err == nil {
			t.Fatal("replacement between Lstat and Open was accepted")
		}
	})

	t.Run("same inode symlink replacement", func(t *testing.T) {
		original := writeTokenFile(t, "")
		moved := filepath.Join(filepath.Dir(original), "moved-token")
		if _, err := readOperatorTokenWith(original, func(path string) (*os.File, error) {
			if err := os.Rename(path, moved); err != nil {
				return nil, err
			}
			if err := os.Symlink(moved, path); err != nil {
				return nil, err
			}
			return os.Open(path)
		}); err == nil {
			t.Fatal("same-inode symlink replacement between Lstat and Open was accepted")
		}
	})
}

func TestRunStartsAndStopsOnCallerCancellation(t *testing.T) {
	tokenPath := writeTokenFile(t, "\r\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, validArgs(tokenPath)) }()
	select {
	case err := <-done:
		t.Fatalf("run returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run after cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not join after cancellation")
	}
}

func TestActualMainSignalAndMalformedArgumentsAreSecretFree(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Abs(): %v", err)
	}
	binary := filepath.Join(t.TempDir(), "lobby-relay")
	build := exec.Command(filepath.Join(root, ".tools", "go", "bin", "go"), "build", "-o", binary, "./cmd/lobby-relay")
	build.Dir = root
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".cache", "go-build"), "GOMODCACHE="+filepath.Join(root, ".cache", "go-mod"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual main: %v\n%s", err, output)
	}

	tokenPath := writeTokenFile(t, "")
	args := validArgs(tokenPath)
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start actual main: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		t.Fatalf("valid main exited during startup: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	// ponytail: :0 listeners expose no readiness address; bounded liveness avoids a production-only test seam.
	case <-time.After(3 * time.Second):
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("SIGTERM actual main: %v", err)
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("actual main after SIGTERM = %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	case <-time.After(2 * time.Second):
		_ = command.Process.Kill()
		<-wait
		t.Fatal("actual main did not exit after SIGTERM")
	}

	malformed := exec.Command(binary, "--unknown")
	malformedOutput, malformedErr := malformed.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(malformedErr, &exitError) || exitError.ExitCode() == 0 {
		t.Fatalf("malformed main = %v output=%q, want non-zero", malformedErr, malformedOutput)
	}
	allOutput := append(append(stdout.Bytes(), stderr.Bytes()...), malformedOutput...)
	for _, sentinel := range [][]byte{
		[]byte(base64.RawURLEncoding.EncodeToString(cliTestToken[:])),
		cliTestToken[:],
		[]byte(hex.EncodeToString(cliTestToken[:])),
	} {
		if bytes.Contains(allOutput, sentinel) {
			t.Fatalf("process output disclosed token form %q", sentinel)
		}
	}
}

func writeTokenFile(t *testing.T, suffix string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "operator-token")
	body := base64.RawURLEncoding.EncodeToString(cliTestToken[:]) + suffix
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	return path
}

func validArgs(tokenPath string) []string {
	return []string{
		"--management-listen", "127.0.0.1:0",
		"--player-listen", "127.0.0.1:0",
		"--relay-network", "udp4",
		"--relay-listen", "127.0.0.1:0",
		"--advertised-host", "relay.test",
		"--advertised-port", "30000",
		"--operator-token-file", tokenPath,
	}
}

func removeFlag(args []string, name string) []string {
	result := make([]string, 0, len(args)-2)
	for index := 0; index < len(args); index += 2 {
		if args[index] != name {
			result = append(result, args[index], args[index+1])
		}
	}
	return result
}

func replaceFlag(args []string, name, value string) []string {
	result := append([]string(nil), args...)
	for index := 0; index < len(result); index += 2 {
		if result[index] == name {
			result[index+1] = value
			return result
		}
	}
	panic("missing test flag " + name)
}

func flagValue(args []string, name string) string {
	for index := 0; index < len(args); index += 2 {
		if args[index] == name {
			return args[index+1]
		}
	}
	panic("missing test flag " + name)
}
