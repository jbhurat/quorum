package params

import (
	"testing"
)

func TestQuorumAndGethVersionWithCommit(t *testing.T) {
	version := QuorumAndGethVersionWithCommit("12345abcdef")
	expectedVersion := "2.1.0-12345abc/Geth-v1.7.2-stable"
	if version != expectedVersion {
		t.Errorf("Got %s QuorumAndGethVersion, want %s", version, expectedVersion)
	}
}

func TestQuorumAndGethVersion(t *testing.T) {
	version := QuorumAndGethVersionWithCommit("")
	expectedVersion := "2.1.0/Geth-v1.7.2-stable"
	if version != expectedVersion {
		t.Errorf("Got %s QuorumAndGethVersion, want %s", version, expectedVersion)
	}
}
