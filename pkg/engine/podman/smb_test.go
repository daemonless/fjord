package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNsmbSectionManagedBlock(t *testing.T) {
	dir := t.TempDir()
	nsmbConf = filepath.Join(dir, "nsmb.conf")
	defer func() { nsmbConf = "/etc/nsmb.conf" }()
	os.WriteFile(nsmbConf, []byte("# user's own config\n[default]\nworkgroup=HOME\n"), 0o600)

	if err := writeNsmbSection("mars", "alice", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := writeNsmbSection("mars", "alice", "changed"); err != nil { // rewrite, not duplicate
		t.Fatal(err)
	}
	if err := writeNsmbSection("nas2", "bob", "pw"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(nsmbConf)
	got := string(b)
	for _, want := range []string{"[default]\nworkgroup=HOME", "[MARS]\naddr=mars", "[MARS:ALICE]", "[NAS2:BOB]", nsmbBegin, nsmbEnd} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "[MARS:ALICE]") != 1 || strings.Count(got, nsmbBegin) != 1 {
		t.Errorf("sections duplicated:\n%s", got)
	}
	if strings.Contains(got, "password=s3cret") {
		t.Errorf("old password survived the rewrite:\n%s", got)
	}
	if !hasSMBCredentials("mars", "alice") || hasSMBCredentials("mars", "nobody") {
		t.Errorf("hasSMBCredentials wrong")
	}
	if fi, _ := os.Stat(nsmbConf); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
}

// A server or username from a request body must never steer the credential
// file out of SMBCredDir, nor forge a line inside it.
func TestCheckSMBIdentityRejectsTraversalAndInjection(t *testing.T) {
	bad := []struct{ server, user string }{
		{"../../etc", "bob"},
		{"nas", "../../../root/.ssh/authorized_keys"},
		{"nas/../..", "bob"},
		{"nas\naddr=10.0.0.1", "bob"},
		{"nas", "bob\npassword=hunter2"},
		{"nas", "bob]\n[NAS:ROOT"},
		{"", "bob"},
		{"nas", ""},
	}
	for _, c := range bad {
		if err := checkSMBIdentity(c.server, c.user); err == nil {
			t.Errorf("accepted server=%q user=%q", c.server, c.user)
		}
	}
	for _, c := range []struct{ server, user string }{
		{"nas", "bob"},
		{"nas.ahze.lan", "DOMAIN\\bob"},
		{"10.0.0.5", "svc_backup"},
		{"truenas-01", "bob@ahze.net"},
	} {
		if err := checkSMBIdentity(c.server, c.user); err != nil {
			t.Errorf("rejected server=%q user=%q: %v", c.server, c.user, err)
		}
	}
}

// Whatever survives validation must stay inside SMBCredDir.
func TestSMBCredFileStaysInDir(t *testing.T) {
	for _, c := range []struct{ server, user string }{
		{"nas", "bob"},
		{"nas.ahze.lan", "DOMAIN\\bob"},
	} {
		got := smbCredFile(c.server, c.user)
		if filepath.Dir(got) != SMBCredDir {
			t.Errorf("%s escaped %s", got, SMBCredDir)
		}
	}
}

// A newline in the password would end the "password=" line.
func TestEnsureSMBCredentialsRejectsNewlinePassword(t *testing.T) {
	err := ensureSMBCredentials("nas", "bob", "hunter2\n[NAS:ROOT]\npassword=x")
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("want a newline refusal, got %v", err)
	}
}
