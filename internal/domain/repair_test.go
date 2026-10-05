package domain

import "testing"

func TestRepairActionsAreClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range RepairActions() {
		if !ValidRepairAction(a) || seen[a] {
			t.Fatalf("action %q invalid or repeated", a)
		}
		seen[a] = true
	}
	if len(seen) != 7 || ValidRepairAction("") || ValidRepairAction("rebuild") || ValidRepairAction("Stats") {
		t.Fatalf("action set: %v", seen)
	}
	for _, a := range RepairActions() {
		if RepairRevertible(a) != (a == RepairStats || a == RepairCounts) {
			t.Fatalf("revertible %s", a)
		}
	}
}
