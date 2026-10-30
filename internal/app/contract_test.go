package app

import (
	"os"
	"strings"
	"testing"
)

// docs/NOTIFY_CONTRACT.md (the copy of crm-ashbyte/docs/NOTIFY_CONTRACT.md) and the version the app speaks must agree.
func TestContractVersion(t *testing.T) {
	b, err := os.ReadFile("../../docs/NOTIFY_CONTRACT.md")
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(b), "\n")
	if !strings.Contains(head, "версия **"+Contract+"**") {
		t.Fatalf("Contract = %s, docs/NOTIFY_CONTRACT.md: %s", Contract, head)
	}
}
